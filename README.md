# 🚀 Self-Healing Infrastructure

**Autonomous Kubernetes self-healing system powered by Google Jules AI Agent.**

When a pod crashes (OOMKilled, CrashLoopBackOff), this system automatically detects the failure, sends context to Jules AI, generates a fix, creates a GitHub PR, and ArgoCD deploys the fix — all without human intervention.

## Architecture

```
K8s Cluster
   ↓
Watcher (Go service inside cluster)
   ↓
Detect OOM / CrashLoopBackOff
   ↓
Fetch real container logs
   ↓
Jules AI Agent (analyze + generate fix)
   ↓
Generate fix (YAML patch)
   ↓
Push to GitHub repo (auto PR)
   ↓
ArgoCD sync
   ↓
Fix applied automatically ✅
```

## What Makes This Unique

This is **NOT** "AI suggests code". This is a **closed-loop autonomous SRE system**:
- **Codex/Copilot** = you ask → they reply → you act (human in the loop)
- **This system** = system detects → Jules decides → PR created → auto-deployed (human is optional)

Jules is an **autonomous agent** that clones your repo, understands full codebase context, makes multi-file changes, and creates PRs — designed to be embedded in production systems.

## Project Structure

```
self-healing-infra/
├── cmd/analyzer/main.go          # Entry point — event loop
├── internal/
│   ├── k8s/watcher.go            # Watches pods for failures (with dedup + reconnect)
│   ├── jules/client.go           # Jules AI client (retry + JSON extraction)
│   ├── git/github.go             # Git clone, branch, commit, push, PR creation
│   ├── fix/patch.go              # Writes YAML patches to repo
│   ├── logs/fetcher.go           # Fetches real container logs from K8s API
│   └── types/types.go            # Shared types
├── k8s/
│   ├── deployment.yaml           # Self-healing analyzer deployment
│   ├── rbac.yaml                 # ServiceAccount + ClusterRole + Binding
│   ├── secret.yaml               # Secret template for API keys
│   ├── demo-app.yaml             # Intentionally broken app for testing
│   └── argocd-app.yaml           # ArgoCD Application for GitOps
├── Dockerfile                    # Multi-stage build
├── go.mod
└── README.md
```

## Prerequisites

### Tools
```bash
kubectl          # Kubernetes CLI
docker           # Container runtime
gh               # GitHub CLI (for PR creation)
helm             # For ArgoCD installation
```

### Infrastructure
- **Kubernetes cluster** (EKS / GKE / k3s / minikube)
- **GitHub repo** (this repo pushed to GitHub)
- **Jules API access** (Google Jules API key + URL)
- **GitHub Personal Access Token** (with `repo` scope)

## Step-by-Step Setup

### 1. Clone and Push to GitHub

```bash
git clone https://github.com/bishal7679/self-healing-infra.git
cd self-healing-infra
```

### 2. Build and Push Docker Image

```bash
# Build
docker build -t ghcr.io/bishal7679/self-healing-infra:latest .

# Push (login first: docker login ghcr.io)
docker push ghcr.io/bishal7679/self-healing-infra:latest
```

### 3. Create Kubernetes Secrets

```bash
# Create secrets with your actual values
kubectl create secret generic self-healing-secrets \
  --from-literal=jules-api-key="YOUR_JULES_API_KEY" \
  --from-literal=jules-api-url="YOUR_JULES_API_URL" \
  --from-literal=github-token="ghp_YOUR_GITHUB_TOKEN" \
  --from-literal=github-repo-url="https://github.com/bishal7679/self-healing-infra.git"
```

### 4. Deploy RBAC and Service

```bash
kubectl apply -f k8s/rbac.yaml
kubectl apply -f k8s/deployment.yaml
```

### 5. Install ArgoCD

```bash
kubectl create namespace argocd
kubectl apply -n argocd -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml

# Wait for ArgoCD to be ready
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=argocd-server -n argocd --timeout=300s

# Get admin password
kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath="{.data.password}" | base64 -d
```

### 6. Create ArgoCD Application

```bash
kubectl apply -f k8s/argocd-app.yaml
```

### 7. Verify Self-Healing Service is Running

```bash
kubectl get pods -l app=self-healing
kubectl logs -f deployment/self-healing
```

## Testing (Step 13)

### Deploy the Intentionally Broken App

```bash
kubectl apply -f k8s/demo-app.yaml
```

### Watch the Pod Crash

```bash
# See it crash with OOMKilled
kubectl get pods -w

# Verify the failure
kubectl describe pod -l app=demo-app
# Look for: Reason: OOMKilled
```

### Watch Self-Healing System React

```bash
kubectl logs -f deployment/self-healing
```

You should see:
```
🚀 Self-Healing Infra Running...
👁️ Starting pod watcher...
🔴 OOMKilled: default/demo-app-xxxx (container: demo-app)
📋 Fetched 2048 bytes of real container logs
🤖 Calling Jules AI agent...
🧠 Jules analysis — Root cause: Memory limit too low | Risk: low
📝 Patch written to /repo/k8s/demo-app-deployment.yaml
🔀 Creating branch: auto-fix/demo-app/20260413-142305
✅ Fix PR created for default/demo-app — Increased memory limit to 512Mi
```

### Check GitHub

Go to your repo → you should see a new PR with the fix.

### Merge and Watch ArgoCD Deploy

```bash
# Merge the PR (or let auto-merge do it)
# ArgoCD will detect the change and sync automatically

kubectl get pods -l app=demo-app
# Should now show: Running ✅
```

## Safety Features

| Feature | Description |
|---------|-------------|
| **Risk gating** | High-risk fixes are logged but NOT auto-applied |
| **Deduplication** | Same issue won't trigger multiple PRs (10-min window) |
| **Auto-reconnect** | Watcher reconnects if K8s API connection drops |
| **Retry logic** | Jules API calls retry 3 times on failure |
| **JSON extraction** | Handles Jules responses wrapped in markdown/text |
| **Graceful shutdown** | Responds to SIGTERM/SIGINT cleanly |
| **Real logs** | Fetches actual container logs (not just status reasons) |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `JULES_API_KEY` | Google Jules API authentication key |
| `JULES_API_URL` | Jules API endpoint URL |
| `GITHUB_TOKEN` | GitHub PAT with `repo` scope |
| `GITHUB_REPO_URL` | Git clone URL for this repo |

## What Happens End-to-End

| Step | What Happens |
|------|-------------|
| 1. Failure | Pod runs out of memory (OOMKilled) |
| 2. Detection | Watcher catches container status change |
| 3. Log Fetch | Real container logs retrieved from K8s API |
| 4. Analysis | Jules AI analyzes root cause and generates fix |
| 5. Patch | Fixed YAML written to repo |
| 6. PR | Branch created, committed, pushed, PR opened |
| 7. Deploy | ArgoCD detects change, syncs, applies fix |
| 8. Recovery | Pod restarts with correct resources ✅ |

## License

MIT
