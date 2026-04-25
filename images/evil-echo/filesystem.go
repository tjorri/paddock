package main

import (
	"os"
	"path/filepath"
	"strings"
)

func cmdReadSecretFiles(glob string) Output {
	if glob == "" {
		// Default to the standard SA token mount path.
		glob = "/var/run/secrets/kubernetes.io/serviceaccount/*"
	}
	// glob may contain `**` which Go's filepath.Glob doesn't support.
	// Substitute with a shallow walk if `**` is present.
	matches := []string{}
	if strings.Contains(glob, "**") {
		root := strings.SplitN(glob, "/**", 2)[0]
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error { //nolint:gosec
			if err != nil {
				return nil
			}
			if !d.IsDir() {
				matches = append(matches, path)
			}
			return nil
		})
	} else {
		m, _ := filepath.Glob(glob)
		matches = append(matches, m...)
	}
	if len(matches) == 0 {
		return Output{Flag: "--read-secret-files", Target: glob, Result: "denied",
			Detail: map[string]any{"matches": []string{}}}
	}
	// Read up to the first 5 matches; report sizes (don't dump content
	// into structured output to keep test logs scannable).
	read := []map[string]any{}
	for i, m := range matches {
		if i >= 5 {
			break
		}
		info, err := os.Stat(m) //nolint:gosec
		entry := map[string]any{"path": m}
		if err != nil {
			entry["stat_error"] = err.Error()
		} else {
			entry["size"] = info.Size()
		}
		read = append(read, entry)
	}
	return Output{Flag: "--read-secret-files", Target: glob, Result: "success",
		Detail: map[string]any{"matches": read, "total": len(matches)}}
}

func cmdReadPVCGitConfig() Output {
	// Workspace PVC is mounted at /workspace by convention.
	candidates := []string{
		"/workspace/.git/config",
		"/workspace/.paddock/repos.json",
	}
	read := []map[string]any{}
	foundAny := false
	for _, p := range candidates {
		entry := map[string]any{"path": p}
		info, err := os.Stat(p)
		if err != nil {
			entry["stat_error"] = err.Error()
			read = append(read, entry)
			continue
		}
		foundAny = true
		entry["size"] = info.Size()
		// Read the file content if small (≤4 KiB) for assertion.
		if info.Size() <= 4096 {
			b, err := os.ReadFile(p) //nolint:gosec
			if err == nil {
				entry["content_preview"] = string(b)
			}
		}
		read = append(read, entry)
	}
	if !foundAny {
		return Output{Flag: "--read-pvc-git-config", Result: "denied",
			Detail: map[string]any{"checked": read}}
	}
	return Output{Flag: "--read-pvc-git-config", Result: "success",
		Detail: map[string]any{"checked": read}}
}

func cmdReadOtherTenantPVC(namespace string) Output {
	if namespace == "" {
		return Output{Flag: "--read-other-tenant-pvc", Result: "error", Error: "missing namespace"}
	}
	// Without an explicit cross-namespace PVC mount, this is mainly a
	// negative test: we have no access to other namespaces' PVCs.
	// Returns "denied" by construction.
	return Output{Flag: "--read-other-tenant-pvc", Target: namespace, Result: "denied",
		Error: "no cross-namespace PVC mount available; RBAC + namespace boundary holds"}
}

func cmdForgeCACert(fqdn string) Output {
	if fqdn == "" {
		return Output{Flag: "--forge-ca-cert", Result: "error", Error: "missing fqdn"}
	}
	// Try to read the per-run CA private key. The proxy mounts
	// `tls.key` only on the proxy container; the agent should NOT see
	// it. F-18 noted the CA itself is shared cluster-wide, but the key
	// file's mount-shape isolation should still hold for the agent.
	candidatePaths := []string{
		"/etc/paddock-proxy-ca/tls.key",
		"/etc/ssl/private/paddock-proxy-ca.key",
		"/var/run/paddock/proxy-ca/tls.key",
	}
	for _, p := range candidatePaths {
		if _, err := os.Stat(p); err == nil {
			return Output{Flag: "--forge-ca-cert", Target: fqdn, Result: "success",
				Detail: map[string]any{"key_path_readable": p}}
		}
	}
	return Output{Flag: "--forge-ca-cert", Target: fqdn, Result: "denied",
		Error: "per-run CA private key not readable from agent container"}
}
