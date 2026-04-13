package types

import "time"

// Anomaly represents a detected Kubernetes issue.
type Anomaly struct {
	Service    string
	Namespace  string
	Issue      string // e.g. "OOMKilled", "CrashLoopBackOff"
	Logs       string // recent container logs
	Timestamp  time.Time
	PodName    string
	Container  string
	RestartCnt int32
}

// JulesResponse is the result from a Jules session.
type JulesResponse struct {
	RootCause string `json:"root_cause"`
	Fix       string `json:"fix"`
	Risk      string `json:"risk"` // "low", "medium", "high"
	PRURL     string `json:"pr_url,omitempty"`
}
