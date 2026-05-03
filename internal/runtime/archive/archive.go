// Package archive owns the per-run workspace directory layout used
// for durable transcript persistence. Each run's directory lives at
// /workspace/.paddock/runs/<run-name>/ and contains:
//
//   - metadata.json  (this package)
//   - events.jsonl   (transcript package writes; archive package only
//     declares the path)
//   - raw.jsonl      (existing harness output, unchanged)
//
// The .paddock/ prefix avoids colliding with user-authored workspace
// files.
package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MetadataSchemaVersion is the current shape of metadata.json. Bumped
// only on incompatible changes; readers should ignore unknown fields.
const MetadataSchemaVersion = "1"

type RunRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	UID       string `json:"uid"`
}

type HarnessRef struct {
	Image       string `json:"image"`
	ImageDigest string `json:"imageDigest,omitempty"`
}

type Metadata struct {
	SchemaVersion string     `json:"schemaVersion"`
	Run           RunRef     `json:"run"`
	Workspace     string     `json:"workspace"`
	Template      string     `json:"template"`
	Mode          string     `json:"mode"`
	Harness       HarnessRef `json:"harness"`
	StartedAt     time.Time  `json:"startedAt"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
	ExitStatus    string     `json:"exitStatus,omitempty"`
	ExitReason    string     `json:"exitReason,omitempty"`
}

// Archive is the per-run handle for the workspace archive directory.
// Construct with Open(); call WriteStartMetadata() once at startup,
// UpdateCompletion() once on agent exit. Concurrent writes from the
// same Archive are serialized internally.
type Archive struct {
	dir string
}

// Open ensures the directory exists and returns a handle. The directory
// is /workspace/.paddock/runs/<runName> by convention; pass the full
// path to allow tests to use a tempdir.
func Open(dir string) (*Archive, error) {
	// 0o755: the run archive lives in /workspace/.paddock/runs/<run>/
	// inside a Pod-local volume. Sibling containers in the same Pod
	// (controller-side collectors, future readers) need read+exec on
	// the directory to walk the archive. Matches the convention used
	// by the existing adapter/collector writers documented in
	// .golangci.yml's gosec G30[126] exclusion for the sibling
	// packages.
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: see rationale above
		return nil, fmt.Errorf("archive: mkdir %s: %w", dir, err)
	}
	return &Archive{dir: dir}, nil
}

// EventsPath returns the absolute path to events.jsonl in this archive.
// Used by the transcript package as its single writer destination.
func (a *Archive) EventsPath() string {
	return filepath.Join(a.dir, "events.jsonl")
}

// MetadataPath returns the absolute path to metadata.json.
func (a *Archive) MetadataPath() string {
	return filepath.Join(a.dir, "metadata.json")
}

// WriteStartMetadata writes metadata.json with StartedAt set and
// CompletedAt nil. Replaces any prior file (a re-running runtime on
// pod restart will overwrite — the start timestamp reflects the most
// recent activation, which is the operator's question).
func (a *Archive) WriteStartMetadata(m Metadata) error {
	m.SchemaVersion = MetadataSchemaVersion
	if m.StartedAt.IsZero() {
		m.StartedAt = time.Now().UTC()
	}
	m.CompletedAt = nil
	m.ExitStatus = ""
	m.ExitReason = ""
	return a.writeMetadata(m)
}

// UpdateCompletion reads the existing metadata.json, sets the
// completion fields, and rewrites atomically. If metadata.json doesn't
// exist (start failed), returns an error so the caller can decide
// whether to log-and-continue.
func (a *Archive) UpdateCompletion(completedAt time.Time, exitStatus, exitReason string) error {
	raw, err := os.ReadFile(a.MetadataPath())
	if err != nil {
		return fmt.Errorf("archive: read metadata: %w", err)
	}
	var m Metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("archive: decode metadata: %w", err)
	}
	m.CompletedAt = &completedAt
	m.ExitStatus = exitStatus
	m.ExitReason = exitReason
	return a.writeMetadata(m)
}

func (a *Archive) writeMetadata(m Metadata) error {
	tmp := a.MetadataPath() + ".tmp"
	// 0o644: same Pod-local audit-trail rationale as Open() above —
	// sibling containers must be able to read metadata.json.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec // G302: see rationale above
	if err != nil {
		return fmt.Errorf("archive: open tmp: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&m); err != nil {
		_ = f.Close()
		return fmt.Errorf("archive: encode metadata: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("archive: close tmp: %w", err)
	}
	if err := os.Rename(tmp, a.MetadataPath()); err != nil {
		return fmt.Errorf("archive: rename: %w", err)
	}
	return nil
}
