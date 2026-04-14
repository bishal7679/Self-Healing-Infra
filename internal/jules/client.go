package jules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"self-healing-infra/internal/types"
)

const (
	julesBaseURL = "https://jules.googleapis.com/v1alpha"
)

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// --- Jules API request/response types ---

// CreateSessionRequest is the body sent to POST /v1alpha/sessions
type CreateSessionRequest struct {
	Prompt         string        `json:"prompt"`
	SourceContext  SourceContext `json:"sourceContext"`
	AutomationMode string        `json:"automationMode"`
	Title          string        `json:"title"`
}

type SourceContext struct {
	Source            string        `json:"source"`
	GithubRepoContext GithubRepoCtx `json:"githubRepoContext"`
}

type GithubRepoCtx struct {
	StartingBranch string `json:"startingBranch"`
}

// SessionResponse is the response from Jules session endpoints
type SessionResponse struct {
	Name    string   `json:"name"`
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Prompt  string   `json:"prompt"`
	Outputs []Output `json:"outputs,omitempty"`
}

type Output struct {
	PullRequest *PullRequest `json:"pullRequest,omitempty"`
}

type PullRequest struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// CallJules creates a Jules session to fix the detected anomaly.
// Jules autonomously: clones the repo → analyzes the issue → creates a PR.
// Returns the session info and PR URL once complete.
func CallJules(a types.Anomaly) (*types.JulesResponse, error) {
	apiKey := os.Getenv("JULES_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("JULES_API_KEY environment variable must be set")
	}

	source := os.Getenv("JULES_SOURCE")
	if source == "" {
		return nil, fmt.Errorf("JULES_SOURCE environment variable must be set (e.g. sources/github/bishal7679/self-healing-infra)")
	}

	branch := os.Getenv("JULES_BRANCH")
	if branch == "" {
		branch = "main"
	}

	// Build the prompt for Jules
	prompt := fmt.Sprintf(`A Kubernetes pod is failing and needs an automated fix.

Service:   %s
Namespace: %s
Issue:     %s
Container: %s
Restarts:  %d
Logs:
%s

IMPORTANT CONSTRAINTS:
- The cluster runs on a single node with only 1 CPU total
- CPU requests across all pods must stay under 1000m total
- Keep CPU requests as low as possible (5m-10m) and CPU limits under 100m

Please fix this issue by modifying ONLY the file k8s/demo-app.yaml.

If the issue is OOMKilled:
- Increase the memory limits (e.g. from 64Mi to 512Mi)
- Increase memory requests proportionally
- Keep CPU requests at 5m and CPU limits at 50m (node is CPU constrained)

If the issue is CrashLoopBackOff:
- Analyze the logs and fix the root cause
- Keep CPU requests at 5m and CPU limits at 50m

Rules:
- ONLY modify k8s/demo-app.yaml
- Do NOT modify any Go source code
- Do NOT create new files
- Do NOT modify k8s/deployment.yaml or k8s/rbac.yaml`,
		a.Service, a.Namespace, a.Issue, a.Container, a.RestartCnt, a.Logs)

	title := fmt.Sprintf("Auto-fix: %s in %s/%s", a.Issue, a.Namespace, a.Service)

	// Step 1: Create a Jules session
	log.Printf("🤖 Creating Jules session for %s/%s...", a.Namespace, a.Service)
	session, err := createSession(apiKey, source, branch, prompt, title)
	if err != nil {
		return nil, fmt.Errorf("failed to create Jules session: %w", err)
	}
	log.Printf("📋 Jules session created: %s (ID: %s)", session.Title, session.ID)

	// Step 2: Poll for completion (Jules works asynchronously)
	log.Println("⏳ Waiting for Jules to analyze and fix...")
	result, err := pollSession(apiKey, session.ID)
	if err != nil {
		return nil, fmt.Errorf("failed while waiting for Jules: %w", err)
	}

	// Build response
	resp := &types.JulesResponse{
		RootCause: a.Issue,
		Fix:       title,
		Risk:      "low",
	}

	// Check if Jules created a PR
	for _, output := range result.Outputs {
		if output.PullRequest != nil {
			resp.PRURL = output.PullRequest.URL
			resp.Fix = output.PullRequest.Description
			log.Printf("🔗 Jules created PR: %s", output.PullRequest.URL)
		}
	}

	return resp, nil
}

// createSession sends POST /v1alpha/sessions to start a Jules task
func createSession(apiKey, source, branch, prompt, title string) (*SessionResponse, error) {
	reqBody := CreateSessionRequest{
		Prompt: prompt,
		SourceContext: SourceContext{
			Source: source,
			GithubRepoContext: GithubRepoCtx{
				StartingBranch: branch,
			},
		},
		AutomationMode: "AUTO_CREATE_PR",
		Title:          title,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", julesBaseURL+"/sessions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jules API returned status %d: %s", resp.StatusCode, string(body))
	}

	var session SessionResponse
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session response: %w", err)
	}

	return &session, nil
}

// pollSession checks the session status every 30 seconds until Jules completes.
// Max wait: 10 minutes.
func pollSession(apiKey, sessionID string) (*SessionResponse, error) {
	maxAttempts := 20 // 20 * 30s = 10 minutes
	for i := 0; i < maxAttempts; i++ {
		if i > 0 {
			time.Sleep(30 * time.Second)
		}

		session, err := getSession(apiKey, sessionID)
		if err != nil {
			log.Printf("⚠️ Error polling session: %v (retrying...)", err)
			continue
		}

		// Check if Jules has produced outputs (PR created)
		if len(session.Outputs) > 0 {
			return session, nil
		}

		log.Printf("⏳ Jules still working... (poll %d/%d)", i+1, maxAttempts)
	}

	return nil, fmt.Errorf("jules session timed out after 10 minutes")
}

// getSession sends GET /v1alpha/sessions/{id}
func getSession(apiKey, sessionID string) (*SessionResponse, error) {
	url := fmt.Sprintf("%s/sessions/%s", julesBaseURL, sessionID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Goog-Api-Key", apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var session SessionResponse
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, err
	}

	return &session, nil
}
