package jules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"self-healing-infra/internal/types"
)

const (
	julesBaseURL = "https://jules.googleapis.com/v1alpha"
)

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// --- Jules API types ---

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

// CallJules creates a Jules session with full diagnostic context.
// NO hardcoded prompts — Jules uses the raw diagnostics (logs, events, spec)
// to autonomously determine root cause and fix the correct manifest file.
func CallJules(a types.Anomaly) (*types.JulesResponse, error) {
	apiKey := os.Getenv("JULES_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("JULES_API_KEY not set")
	}

	source := os.Getenv("JULES_SOURCE")
	if source == "" {
		return nil, fmt.Errorf("JULES_SOURCE not set")
	}

	branch := os.Getenv("JULES_BRANCH")
	if branch == "" {
		branch = "main"
	}

	// Build a purely diagnostic prompt — no solution hints, Jules figures it out
	prompt := buildPrompt(a)

	title := fmt.Sprintf("fix(%s): %s in %s/%s",
		a.Namespace, strings.ToLower(a.Issue), a.OwnerKind, a.OwnerName)

	log.Printf("🤖 Creating Jules session for %s/%s (owner: %s/%s)...",
		a.Namespace, a.PodName, a.OwnerKind, a.OwnerName)

	session, err := createSession(apiKey, source, branch, prompt, title)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	log.Printf("📋 Session created: %s (ID: %s)", session.Title, session.ID)
	log.Println("⏳ Waiting for Jules to analyze and create a fix...")

	result, err := pollSession(apiKey, session.ID)
	if err != nil {
		return nil, fmt.Errorf("poll session: %w", err)
	}

	resp := &types.JulesResponse{
		RootCause: a.Issue,
		Fix:       "Jules analyzed but produced no PR",
		SessionID: session.ID,
	}

	for _, output := range result.Outputs {
		if output.PullRequest != nil {
			resp.PRURL = output.PullRequest.URL
			resp.Fix = output.PullRequest.Description
			log.Printf("🔗 PR created: %s", output.PullRequest.URL)
		}
	}

	return resp, nil
}

// buildPrompt generates a context-rich diagnostic dump for Jules.
// It contains ZERO solution hints — Jules determines the fix autonomously.
func buildPrompt(a types.Anomaly) string {
	var b strings.Builder

	b.WriteString("## Kubernetes Pod Failure — Automated Diagnosis\n\n")

	// Identity
	b.WriteString(fmt.Sprintf("**Pod:**       %s\n", a.PodName))
	b.WriteString(fmt.Sprintf("**Namespace:** %s\n", a.Namespace))
	b.WriteString(fmt.Sprintf("**Container:** %s\n", a.Container))
	b.WriteString(fmt.Sprintf("**Owner:**     %s/%s\n", a.OwnerKind, a.OwnerName))
	b.WriteString(fmt.Sprintf("**Issue:**     %s\n", a.Issue))
	if a.Reason != "" {
		b.WriteString(fmt.Sprintf("**Reason:**    %s\n", a.Reason))
	}
	b.WriteString(fmt.Sprintf("**Exit Code:** %d\n", a.ExitCode))
	b.WriteString(fmt.Sprintf("**Restarts:**  %d\n", a.RestartCnt))
	b.WriteString("\n")

	// Container spec
	if a.PodSpec != "" {
		b.WriteString("### Container Spec\n```\n")
		b.WriteString(a.PodSpec)
		b.WriteString("```\n\n")
	}

	// Events
	if a.PodEvents != "" {
		b.WriteString("### Kubernetes Events\n```\n")
		b.WriteString(a.PodEvents)
		b.WriteString("```\n\n")
	}

	// Logs
	if a.ContainerLogs != "" {
		b.WriteString("### Container Logs\n```\n")
		b.WriteString(a.ContainerLogs)
		b.WriteString("```\n\n")
	}

	// Manifest hint
	b.WriteString("### Instructions\n")
	if a.ManifestPath != "" {
		b.WriteString(fmt.Sprintf("The Kubernetes manifest for this workload is likely at `%s`.\n", a.ManifestPath))
	}
	b.WriteString("Analyze the diagnostics above, determine the root cause, and fix the relevant Kubernetes manifest YAML file(s) in this repository.\n")
	b.WriteString("Do NOT modify any application source code — only Kubernetes YAML manifests.\n")

	return b.String()
}

// createSession sends POST /v1alpha/sessions
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
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", julesBaseURL+"/sessions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API status %d: %s", resp.StatusCode, string(body))
	}

	var session SessionResponse
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	return &session, nil
}

// pollSession checks every 30s until Jules finishes (max 15 min).
func pollSession(apiKey, sessionID string) (*SessionResponse, error) {
	maxAttempts := 30
	for i := 0; i < maxAttempts; i++ {
		if i > 0 {
			time.Sleep(30 * time.Second)
		}

		session, err := getSession(apiKey, sessionID)
		if err != nil {
			log.Printf("⚠️ Poll error: %v (retrying...)", err)
			continue
		}

		if len(session.Outputs) > 0 {
			return session, nil
		}

		log.Printf("⏳ Jules working... (poll %d/%d)", i+1, maxAttempts)
	}

	return nil, fmt.Errorf("session timed out after 15 minutes")
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
