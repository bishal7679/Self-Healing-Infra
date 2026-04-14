package logs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"self-healing-infra/internal/types"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Fetcher retrieves container logs, events, and pod details from the K8s API.
type Fetcher struct {
	clientset   *kubernetes.Clientset
	manifestDir string // base dir in repo for K8s manifests (e.g. "k8s")
}

// NewFetcher creates a new fetcher using in-cluster config.
func NewFetcher() (*Fetcher, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	manifestDir := os.Getenv("MANIFEST_DIR")
	if manifestDir == "" {
		manifestDir = "k8s"
	}

	return &Fetcher{clientset: clientset, manifestDir: manifestDir}, nil
}

// Enrich populates an Anomaly with container logs, K8s events, pod spec, and manifest path.
func (f *Fetcher) Enrich(a *types.Anomaly) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Container logs (previous + current)
	a.ContainerLogs = f.getLogs(ctx, a.Namespace, a.PodName, a.Container)

	// 2. K8s events for this pod
	a.PodEvents = f.getEvents(ctx, a.Namespace, a.PodName)

	// 3. Pod spec summary (resources, image, args)
	a.PodSpec = f.getPodSpec(ctx, a.Namespace, a.PodName, a.Container)

	// 4. Guess manifest file path in repo
	a.ManifestPath = f.guessManifestPath(a.OwnerName)
}

// getLogs fetches recent container logs.
func (f *Fetcher) getLogs(ctx context.Context, ns, pod, container string) string {
	var tailLines int64 = 80

	// Try previous (crashed) container first
	opts := &v1.PodLogOptions{
		Container: container,
		Previous:  true,
		TailLines: &tailLines,
	}
	logs, err := f.fetchLogs(ctx, ns, pod, opts)
	if err != nil {
		// Fallback to current container
		opts.Previous = false
		logs, err = f.fetchLogs(ctx, ns, pod, opts)
		if err != nil {
			log.Printf("⚠️ Could not fetch logs for %s/%s/%s: %v", ns, pod, container, err)
			return ""
		}
	}

	const maxBytes = 6000
	if len(logs) > maxBytes {
		logs = logs[len(logs)-maxBytes:]
	}
	return logs
}

func (f *Fetcher) fetchLogs(ctx context.Context, ns, pod string, opts *v1.PodLogOptions) (string, error) {
	stream, err := f.clientset.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
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

// getEvents fetches recent K8s events for a pod.
func (f *Fetcher) getEvents(ctx context.Context, ns, podName string) string {
	events, err := f.clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s", podName),
	})
	if err != nil {
		log.Printf("⚠️ Could not fetch events for %s/%s: %v", ns, podName, err)
		return ""
	}

	var sb strings.Builder
	for _, e := range events.Items {
		sb.WriteString(fmt.Sprintf("[%s] %s: %s\n", e.Type, e.Reason, e.Message))
	}

	result := sb.String()
	if len(result) > 3000 {
		result = result[len(result)-3000:]
	}
	return result
}

// getPodSpec returns a human-readable summary of the container spec.
func (f *Fetcher) getPodSpec(ctx context.Context, ns, podName, containerName string) string {
	pod, err := f.clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		log.Printf("⚠️ Could not fetch pod spec for %s/%s: %v", ns, podName, err)
		return ""
	}

	for _, c := range pod.Spec.Containers {
		if c.Name == containerName {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Image: %s\n", c.Image))
			if len(c.Command) > 0 {
				sb.WriteString(fmt.Sprintf("Command: %v\n", c.Command))
			}
			if len(c.Args) > 0 {
				sb.WriteString(fmt.Sprintf("Args: %v\n", c.Args))
			}
			res := c.Resources
			if res.Requests != nil {
				sb.WriteString(fmt.Sprintf("Requests: cpu=%s, memory=%s\n",
					res.Requests.Cpu().String(), res.Requests.Memory().String()))
			}
			if res.Limits != nil {
				sb.WriteString(fmt.Sprintf("Limits: cpu=%s, memory=%s\n",
					res.Limits.Cpu().String(), res.Limits.Memory().String()))
			}
			return sb.String()
		}
	}
	return ""
}

// guessManifestPath infers the YAML manifest path in the repo for a given owner.
// Convention: <manifestDir>/<ownerName>.yaml
func (f *Fetcher) guessManifestPath(ownerName string) string {
	if ownerName == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s.yaml", f.manifestDir, ownerName)
}
