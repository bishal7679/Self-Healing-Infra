package types

import "time"

// Anomaly represents a detected Kubernetes pod failure with full context.
type Anomaly struct {
	// Pod identity
	PodName   string
	Namespace string
	Container string

	// Failure info
	Issue      string // "OOMKilled", "CrashLoopBackOff", "Error", "ImagePullBackOff"
	Reason     string // detailed reason from K8s
	RestartCnt int32
	ExitCode   int32
	Timestamp  time.Time

	// Owner info (Deployment, StatefulSet, DaemonSet, etc.)
	OwnerKind string // "Deployment", "StatefulSet", "ReplicaSet", etc.
	OwnerName string // e.g. "demo-app"

	// Diagnostics collected by the enricher
	ContainerLogs string // recent logs from the crashed container
	PodEvents     string // recent K8s events for this pod
	PodSpec       string // YAML snippet of pod spec (resources, image, etc.)
	ManifestPath  string // path in the repo to the K8s manifest (e.g. "k8s/demo-app.yaml")
}

// JulesResponse is the result from a Jules session.
type JulesResponse struct {
	RootCause string `json:"root_cause"`
	Fix       string `json:"fix"`
	PRURL     string `json:"pr_url,omitempty"`
	SessionID string `json:"session_id"`
}

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// Jules
	JulesAPIKey string
	JulesSource string
	JulesBranch string

	// Namespaces to watch (comma-separated in env, parsed here)
	WatchNamespaces []string

	// Dedup window in minutes
	DedupMinutes int

	// Base directory in repo containing K8s manifests
	ManifestDir string
}
