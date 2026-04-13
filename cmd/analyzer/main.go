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
	fmt.Println("🚀 Self-Healing Infra Running...")
	fmt.Println("   Powered by Google Jules AI Agent")

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("🛑 Received signal %s, shutting down...", sig)
		cancel()
	}()

	// Channel for anomalies
	ch := make(chan types.Anomaly, 10)

	// Start Kubernetes pod watcher
	go k8s.WatchPods(ctx, ch)

	// Start log fetcher (enriches anomalies with real logs)
	logFetcher, err := logfetcher.NewFetcher()
	if err != nil {
		log.Printf("⚠️ Log fetcher unavailable (will use basic logs): %v", err)
	}

	// Main event loop
	for {
		select {
		case <-ctx.Done():
			fmt.Println("🛑 Shutting down gracefully...")
			return

		case anomaly := <-ch:
			go handleAnomaly(anomaly, logFetcher)
		}
	}
}

func handleAnomaly(anomaly types.Anomaly, logFetcher *logfetcher.Fetcher) {
	log.Printf("⚠️ Detected issue: [%s] %s/%s — %s",
		anomaly.Issue, anomaly.Namespace, anomaly.Service, anomaly.Logs)

	// Enrich with real container logs if available
	if logFetcher != nil {
		realLogs, err := logFetcher.GetLogs(anomaly.Namespace, anomaly.PodName, anomaly.Container)
		if err == nil && realLogs != "" {
			anomaly.Logs = realLogs
			log.Printf("📋 Fetched %d bytes of real container logs", len(realLogs))
		}
	}

	// Call Jules — Jules will autonomously:
	// 1. Clone the repo
	// 2. Analyze the issue
	// 3. Fix the YAML
	// 4. Create a PR
	log.Println("🤖 Delegating to Google Jules AI Agent...")
	resp, err := jules.CallJules(anomaly)
	if err != nil {
		log.Printf("❌ Jules error: %v", err)
		return
	}

	if resp.PRURL != "" {
		log.Printf("✅ Jules created PR: %s", resp.PRURL)
		log.Printf("   Fix: %s", resp.Fix)
	} else {
		log.Printf("⚠️ Jules completed but no PR was created")
		log.Printf("   Result: %s", resp.Fix)
	}
}
