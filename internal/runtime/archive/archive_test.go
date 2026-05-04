package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpen_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs", "tuomo-test")
	a, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if a.EventsPath() != filepath.Join(dir, "events.jsonl") {
		t.Fatalf("EventsPath: %s", a.EventsPath())
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestWriteStartMetadata_AndUpdateCompletion(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	started := time.Date(2026, 5, 3, 2, 12, 15, 0, time.UTC)
	if err := a.WriteStartMetadata(Metadata{
		Run:       RunRef{Name: "run", Namespace: "ns", UID: "u"},
		Workspace: "ws", Template: "tpl", Mode: "Interactive",
		Harness:   HarnessRef{Image: "img:dev"},
		StartedAt: started,
	}); err != nil {
		t.Fatalf("WriteStartMetadata: %v", err)
	}
	raw, err := os.ReadFile(a.MetadataPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Metadata
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SchemaVersion != "1" || got.Run.Name != "run" || got.Workspace != "ws" || got.CompletedAt != nil {
		t.Fatalf("start metadata wrong: %#v", got)
	}
	completed := started.Add(5 * time.Minute)
	if err := a.UpdateCompletion(completed, "succeeded", "agent exited cleanly"); err != nil {
		t.Fatalf("UpdateCompletion: %v", err)
	}
	raw, _ = os.ReadFile(a.MetadataPath())
	_ = json.Unmarshal(raw, &got)
	if got.CompletedAt == nil || !got.CompletedAt.Equal(completed) || got.ExitStatus != "succeeded" {
		t.Fatalf("completion not persisted: %#v", got)
	}
}

func TestWriteStartMetadata_StampsStartIfZero(t *testing.T) {
	a, _ := Open(t.TempDir())
	before := time.Now().UTC()
	if err := a.WriteStartMetadata(Metadata{Run: RunRef{Name: "x"}, Mode: "Batch"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(a.MetadataPath())
	var got Metadata
	_ = json.Unmarshal(raw, &got)
	if got.StartedAt.Before(before) {
		t.Fatalf("StartedAt not stamped: %v < %v", got.StartedAt, before)
	}
}

func TestUpdateCompletion_ErrorsWhenMissing(t *testing.T) {
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := a.UpdateCompletion(time.Now(), "succeeded", "ok"); err == nil {
		t.Fatal("UpdateCompletion: expected error when metadata.json absent, got nil")
	}
	if _, statErr := os.Stat(a.MetadataPath()); !os.IsNotExist(statErr) {
		t.Fatalf("UpdateCompletion: metadata.json should not be created on missing-read, got stat err %v", statErr)
	}
}
