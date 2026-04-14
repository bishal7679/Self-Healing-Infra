package k8s

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"self-healing-infra/internal/types"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// recentAlerts tracks recently sent anomalies to avoid duplicates.
// key = "namespace/pod/container/issue"
var (
	recentAlerts = make(map[string]time.Time)
	alertMu      sync.Mutex
	dedupWindow  = 30 * time.Minute
)

// watchedNamespaces are the ONLY namespaces we monitor.
// Everything else (argocd, kube-system, etc.) is ignored automatically.
var watchedNamespaces = map[string]bool{
	"default": true,
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

// WatchPods watches all pods in the cluster and sends anomalies to the channel.
// It automatically reconnects if the watch stream breaks.
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

func checkPod(pod *v1.Pod, ch chan<- types.Anomaly) {
	// ALLOWLIST approach: only watch namespaces we explicitly manage
	if !watchedNamespaces[pod.Namespace] {
		return
	}

	// Skip pods owned by the self-healing system itself
	for _, owner := range pod.OwnerReferences {
		if owner.Name == "self-healing" {
			return
		}
	}

	// Only watch pods labeled for self-healing testing
	if pod.Labels["purpose"] != "self-healing-test" {
		return
	}

	for _, c := range pod.Status.ContainerStatuses {
		// CrashLoopBackOff detection
		if c.State.Waiting != nil && c.State.Waiting.Reason == "CrashLoopBackOff" {
			key := fmt.Sprintf("%s/%s/%s/CrashLoopBackOff", pod.Namespace, pod.Name, c.Name)
			if !isDuplicate(key) {
				log.Printf("🔴 CrashLoopBackOff: %s/%s (container: %s, restarts: %d)",
					pod.Namespace, pod.Name, c.Name, c.RestartCount)
				ch <- types.Anomaly{
					Service:    pod.Name,
					Namespace:  pod.Namespace,
					Issue:      "CrashLoopBackOff",
					Logs:       fmt.Sprintf("Container %s crashing repeatedly, restart count: %d", c.Name, c.RestartCount),
					Timestamp:  time.Now(),
					PodName:    pod.Name,
					Container:  c.Name,
					RestartCnt: c.RestartCount,
				}
			}
		}

		// OOMKilled detection
		if c.LastTerminationState.Terminated != nil &&
			c.LastTerminationState.Terminated.Reason == "OOMKilled" {

			key := fmt.Sprintf("%s/%s/%s/OOMKilled", pod.Namespace, pod.Name, c.Name)
			if !isDuplicate(key) {
				log.Printf("🔴 OOMKilled: %s/%s (container: %s)", pod.Namespace, pod.Name, c.Name)
				ch <- types.Anomaly{
					Service:    pod.Name,
					Namespace:  pod.Namespace,
					Issue:      "OOMKilled",
					Logs:       fmt.Sprintf("Container %s killed due to OOM, restart count: %d", c.Name, c.RestartCount),
					Timestamp:  time.Now(),
					PodName:    pod.Name,
					Container:  c.Name,
					RestartCnt: c.RestartCount,
				}
			}
		}
	}
}
