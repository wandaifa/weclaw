package messaging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAgedFile(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

func TestCleanupDirRemovesOnlyExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	oldPath := writeAgedFile(t, dir, "old.jpg", 200*24*time.Hour)
	newPath := writeAgedFile(t, dir, "new.jpg", 1*time.Hour)

	deleted, err := cleanupDir(dir, time.Now().AddDate(0, 0, -180))
	if err != nil {
		t.Fatalf("cleanupDir returned error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 file deleted, got %d", deleted)
	}
	if _, statErr := os.Stat(oldPath); !os.IsNotExist(statErr) {
		t.Errorf("expected old file to be removed, stat err: %v", statErr)
	}
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Errorf("expected new file to survive, stat err: %v", statErr)
	}
}

func TestCleanupDirEmptyDirNoError(t *testing.T) {
	dir := t.TempDir()
	deleted, err := cleanupDir(dir, time.Now())
	if err != nil {
		t.Fatalf("cleanupDir on empty dir returned error: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 deletions, got %d", deleted)
	}
}

func TestCleanupDirMissingDirNoError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	deleted, err := cleanupDir(dir, time.Now())
	if err != nil {
		t.Fatalf("cleanupDir on missing dir returned error: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 deletions, got %d", deleted)
	}
}

func TestCleanupExpiredMediaScansAllThreeDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := filepath.Join(home, ".weclaw", "workspace")
	writeAgedFile(t, filepath.Join(workspace, "inbound-images"), "old.jpg", 200*24*time.Hour)
	writeAgedFile(t, filepath.Join(workspace, "inbound-videos"), "old.mp4", 200*24*time.Hour)
	writeAgedFile(t, filepath.Join(workspace, "inbound-files"), "old.pdf", 200*24*time.Hour)
	writeAgedFile(t, filepath.Join(workspace, "inbound-files"), "new.pdf", 1*time.Hour)

	deleted, err := CleanupExpiredMedia(180)
	if err != nil {
		t.Fatalf("CleanupExpiredMedia returned error: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 files deleted across all dirs, got %d", deleted)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "inbound-files", "new.pdf")); statErr != nil {
		t.Errorf("expected recent file to survive, stat err: %v", statErr)
	}
}
