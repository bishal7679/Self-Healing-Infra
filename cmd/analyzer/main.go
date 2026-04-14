package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"self-healing-infra/internal/jules"
	"self-healing-infra/internal/k8s"
	logfetcher "self-healing-infra/internal/logs"
	"self-healing-infra/internal/types"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	fmt.Println("🚀 Self-Healing Infrastructure v3")
	fmt.Println("   Powered by Google Jules AI Agent")
	fmt.Printf("   Namespaces: %s\n", os.Getenv("WATCH_NAMESPACES"))

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("🛑 Received %s, shutting down...", sig)
		cancel()
	}()

	// Enricher — fetches logs, events, pod spec
	enricher, err := logfetcher.NewFetcher()
	if err != nil {
		log.Printf("⚠️ Enricher unavailable (will run with basic diagnostics): %v", err)
	}

	// Anomaly channel
	ch := make(chan types.Anomaly, 20)

	// Start watcher (reads WATCH_NAMESPACES from env)
	go k8s.WatchPods(ctx, ch)

	// Write health marker for liveness probe
	os.WriteFile("/tmp/healthy", []byte("ok"), 0644)

	// Main event loop — one goroutine per anomaly
	for {
		select {
		case <-ctx.Done():
			fmt.Println("🛑 Shutting down gracefully...")
			return
		case anomaly := <-ch:
			go handleAnomaly(anomaly, enricher)
		}
	}
}

func handleAnomaly(a types.Anomaly, enricher *logfetcher.Fetcher) {
	log.Printf("⚠️ [%s] %s/%s — %s (owner: %s/%s, restarts: %d)",
		a.Issue, a.Namespace, a.PodName, a.Reason, a.OwnerKind, a.OwnerName, a.RestartCnt)

	// Enrich with full diagnostics
	if enricher != nil {
		enricher.Enrich(&a)
		log.Printf("📋 Enriched: logs=%d bytes, events=%d bytes, spec=%d bytes, manifest=%s",
			len(a.ContainerLogs), len(a.PodEvents), len(a.PodSpec), a.ManifestPath)
	}

	// Delegate to Jules
	log.Printf("🤖 Sending to Jules: %s/%s [%s]", a.Namespace, a.OwnerName, a.Issue)
	resp, err := jules.CallJules(a)
	if err != nil {
		log.Printf("❌ Jules error: %v", err)
		return
	}

	if resp.PRURL != "" {
		log.Printf("✅ Jules created PR: %s", resp.PRURL)
		log.Printf("   Fix: %s", resp.Fix)
	} else {
		log.Printf("⚠️ Jules completed (session %s) but no PR created", resp.SessionID)
	}
}
