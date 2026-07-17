package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendNewCodexGeneratedImages(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	threadID := "thread-1"
	imageDir := filepath.Join(home, ".codex", "generated_images", threadID)
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}

	oldPath := filepath.Join(imageDir, "old.png")
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old image: %v", err)
	}
	existing := snapshotCodexGeneratedImages(threadID)

	newPath := filepath.Join(imageDir, "new.png")
	if err := os.WriteFile(newPath, []byte("new"), 0o644); err != nil {
		t.Fatalf("write new image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "notes.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("write text file: %v", err)
	}

	got := appendNewCodexGeneratedImages("reply", threadID, existing)
	if !strings.Contains(got, "reply\n"+newPath) {
		t.Fatalf("expected reply to include new image path, got %q", got)
	}
	if strings.Contains(got, oldPath) {
		t.Fatalf("expected old image path to be skipped, got %q", got)
	}
}
