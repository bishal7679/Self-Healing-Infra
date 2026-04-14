package k8s

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"self-healing-infra/internal/types"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// dedup tracks recently sent anomalies to avoid duplicate Jules sessions.
// Key = "namespace/ownerName/issue" — dedup is per owner, not per pod replica.
var (
	recentAlerts = make(map[string]time.Time)
	alertMu      sync.Mutex
	dedupWindow  = 30 * time.Minute // default, overridden by DEDUP_MINUTES
)

// watchedNamespaces is populated dynamically from WATCH_NAMESPACES env var.
var watchedNamespaces map[string]bool

func init() {
	// Parse WATCH_NAMESPACES (comma-separated). Default: "default"
	raw := os.Getenv("WATCH_NAMESPACES")
	if raw == "" {
		raw = "default"
	}
	watchedNamespaces = make(map[string]bool)
	for _, ns := range strings.Split(raw, ",") {
		ns = strings.TrimSpace(ns)
		if ns != "" {
			watchedNamespaces[ns] = true
		}
	}
	log.Printf("👁️ Watching namespaces: %v", namespacesSlice())

	// Parse DEDUP_MINUTES
	if dm := os.Getenv("DEDUP_MINUTES"); dm != "" {
		var mins int
		if _, err := fmt.Sscanf(dm, "%d", &mins); err == nil && mins > 0 {
			dedupWindow = time.Duration(mins) * time.Minute
		}
	}
	log.Printf("⏱️ Dedup window: %v", dedupWindow)
}

func namespacesSlice() []string {
	var ns []string
	for k := range watchedNamespaces {
		ns = append(ns, k)
	}
	return ns
}

func isDuplicate(key string) bool {
	alertMu.Lock()
	defer alertMu.Unlock()

	if t, ok := recentAlerts[key]; ok {
		if time.Since(t) < dedupWindow {
			return true
		}
	}
	recentAlerts[key] = time.Now()
	return false
}

// WatchPods watches pods in the configured namespaces and sends anomalies.
// Automatically reconnects on watch stream breaks.
func WatchPods(ctx context.Context, ch chan<- types.Anomaly) {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("❌ Failed to get in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("❌ Failed to create Kubernetes client: %v", err)
	}

	log.Println("👁️ Starting pod watcher...")

	for {
		err := watchLoop(ctx, clientset, ch)
		if err != nil {
			if ctx.Err() != nil {
				log.Println("🛑 Watcher stopped (context cancelled)")
				return
			}
			log.Printf("⚠️ Watch connection lost: %v — reconnecting in 5s...", err)
			time.Sleep(5 * time.Second)
		}
	}
}

func watchLoop(ctx context.Context, clientset *kubernetes.Clientset, ch chan<- types.Anomaly) error {
	// Watch all namespaces; filter in checkPod
	watcher, err := clientset.CoreV1().Pods("").Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to start watch: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}
			if event.Type != watch.Modified && event.Type != watch.Added {
				continue
			}

			pod, ok := event.Object.(*v1.Pod)
			if !ok {
				continue
			}

			checkPod(pod, ch)
		}
	}
}

// resolveOwner walks OwnerReferences to find the top-level controller name/kind.
func resolveOwner(pod *v1.Pod) (string, string) {
	if len(pod.OwnerReferences) == 0 {
		return "Pod", pod.Name
	}
	owner := pod.OwnerReferences[0]
	// ReplicaSet → strip hash suffix to get Deployment name
	if owner.Kind == "ReplicaSet" {
		parts := strings.Split(owner.Name, "-")
		if len(parts) > 2 {
			return "Deployment", strings.Join(parts[:len(parts)-1], "-")
		}
		return "ReplicaSet", owner.Name
	}
	return owner.Kind, owner.Name
}

func checkPod(pod *v1.Pod, ch chan<- types.Anomaly) {
	// Only watch configured namespaces
	if !watchedNamespaces[pod.Namespace] {
		return
	}

	// Skip self-healing pods
	if pod.Labels["app"] == "self-healing" {
		return
	}

	ownerKind, ownerName := resolveOwner(pod)

	for _, c := range pod.Status.ContainerStatuses {
		var issue, reason string
		var exitCode int32

		// 1. OOMKilled — highest priority
		if c.LastTerminationState.Terminated != nil &&
			c.LastTerminationState.Terminated.Reason == "OOMKilled" {
			issue = "OOMKilled"
			reason = "Container was killed because it exceeded its memory limit"
			exitCode = c.LastTerminationState.Terminated.ExitCode
		}

		// 2. CrashLoopBackOff
		if issue == "" && c.State.Waiting != nil && c.State.Waiting.Reason == "CrashLoopBackOff" {
			issue = "CrashLoopBackOff"
			reason = c.State.Waiting.Message
			if c.LastTerminationState.Terminated != nil {
				exitCode = c.LastTerminationState.Terminated.ExitCode
				if reason == "" {
					reason = c.LastTerminationState.Terminated.Reason
				}
			}
		}

		// 3. ImagePullBackOff
		if issue == "" && c.State.Waiting != nil &&
			(c.State.Waiting.Reason == "ImagePullBackOff" || c.State.Waiting.Reason == "ErrImagePull") {
			issue = "ImagePullBackOff"
			reason = c.State.Waiting.Message
		}

		if issue == "" {
			continue
		}

		// Dedup key: namespace/ownerName/container/issue
		// This ensures ONE alert per deployment per issue, not per pod replica
		key := fmt.Sprintf("%s/%s/%s/%s", pod.Namespace, ownerName, c.Name, issue)
		if isDuplicate(key) {
			continue
		}

		log.Printf("🔴 %s: %s/%s (owner: %s/%s, container: %s, restarts: %d)",
			issue, pod.Namespace, pod.Name, ownerKind, ownerName, c.Name, c.RestartCount)

		ch <- types.Anomaly{
			PodName:    pod.Name,
			Namespace:  pod.Namespace,
			Container:  c.Name,
			Issue:      issue,
			Reason:     reason,
			RestartCnt: c.RestartCount,
			ExitCode:   exitCode,
			Timestamp:  time.Now(),
			OwnerKind:  ownerKind,
			OwnerName:  ownerName,
		}
	}
}
