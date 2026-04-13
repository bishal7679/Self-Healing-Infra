package fix

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// WorkDir is the base directory for the git repo inside the container.
// This is where k8s manifests live and where git operations are performed.
var WorkDir = "/repo"

// ApplyPatch writes the Jules-generated YAML to the appropriate file in the repo.
// It sanitizes the service name and validates the YAML is non-empty.
func ApplyPatch(service string, patchYAML string) error {
	if strings.TrimSpace(patchYAML) == "" {
		return fmt.Errorf("empty patch YAML for service %s", service)
	}

	// Sanitize service name (remove namespace prefix if present, etc.)
	safeName := sanitizeName(service)

	dir := filepath.Join(WorkDir, "k8s")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%s-deployment.yaml", safeName))

	// Ensure YAML ends with newline
	if !strings.HasSuffix(patchYAML, "\n") {
		patchYAML += "\n"
	}

	if err := os.WriteFile(path, []byte(patchYAML), 0644); err != nil {
		return fmt.Errorf("failed to write patch to %s: %w", path, err)
	}

	log.Printf("📝 Patch written to %s", path)
	return nil
}

func sanitizeName(name string) string {
	// Remove common suffixes like random pod hashes: demo-app-7f8b9c6d4-x2k9m → demo-app
	parts := strings.Split(name, "-")
	if len(parts) > 2 {
		// Heuristic: if last 2 parts look like k8s hash (short alphanumeric), trim them
		last := parts[len(parts)-1]
		secondLast := parts[len(parts)-2]
		if len(last) <= 5 && len(secondLast) <= 10 && len(parts) > 2 {
			return strings.Join(parts[:len(parts)-2], "-")
		}
	}
	return name
}
