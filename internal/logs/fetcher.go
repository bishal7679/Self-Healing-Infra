package logs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Fetcher retrieves container logs from the Kubernetes API.
type Fetcher struct {
	clientset *kubernetes.Clientset
}

// NewFetcher creates a new log fetcher using in-cluster config.
func NewFetcher() (*Fetcher, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &Fetcher{clientset: clientset}, nil
}

// GetLogs fetches recent logs from a specific container in a pod.
// Returns the last 50 lines or logs from the previous terminated container.
func (f *Fetcher) GetLogs(namespace, podName, container string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var tailLines int64 = 50

	// Try to get logs from the previous (crashed) container first
	opts := &v1.PodLogOptions{
		Container: container,
		Previous:  true,
		TailLines: &tailLines,
	}

	logs, err := f.fetchLogs(ctx, namespace, podName, opts)
	if err != nil {
		log.Printf("⚠️ Could not get previous container logs, trying current: %v", err)
		// Fall back to current container logs
		opts.Previous = false
		logs, err = f.fetchLogs(ctx, namespace, podName, opts)
		if err != nil {
			return "", fmt.Errorf("failed to get logs for %s/%s/%s: %w", namespace, podName, container, err)
		}
	}

	// Truncate to avoid sending too much to Jules
	const maxLogBytes = 4000
	if len(logs) > maxLogBytes {
		logs = logs[len(logs)-maxLogBytes:]
	}

	return logs, nil
}

func (f *Fetcher) fetchLogs(ctx context.Context, namespace, podName string, opts *v1.PodLogOptions) (string, error) {
	req := f.clientset.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil {
		return "", err
	}

	return buf.String(), nil
}
