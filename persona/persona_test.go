package persona

import (
	"path/filepath"
	"testing"
)

func TestEnsureDefaultSeedsFileOnce(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureDefault(dir); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if got := Load(dir, DefaultName); got != fallbackText {
		t.Fatalf("seeded default text = %q, want fallbackText", got)
	}

	if err := Save(dir, DefaultName, "自定义默认人格"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := EnsureDefault(dir); err != nil {
		t.Fatalf("EnsureDefault (second call): %v", err)
	}
	if got := Load(dir, DefaultName); got != "自定义默认人格" {
		t.Fatalf("EnsureDefault overwrote a customized default: got %q", got)
	}
}

func TestLoadFallsBackToSafeTextWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if got := Load(dir, "does-not-exist"); got != fallbackText {
		t.Fatalf("Load(missing) = %q, want fallbackText", got)
	}
}

func TestLoadFallsBackToSafeTextForInvalidName(t *testing.T) {
	dir := t.TempDir()
	if got := Load(dir, "../../etc/passwd"); got != fallbackText {
		t.Fatalf("Load(path-traversal name) = %q, want fallbackText", got)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "vip", "VIP 专属人格"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load(dir, "vip"); got != "VIP 专属人格" {
		t.Fatalf("Load(vip) = %q, want %q", got, "VIP 专属人格")
	}
}

func TestSaveRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "../escape", "x"); err == nil {
		t.Fatal("Save should reject a name containing path separators")
	}
}

func TestListReturnsSortedPersonas(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "zeta", "Z"); err != nil {
		t.Fatalf("Save zeta: %v", err)
	}
	if err := Save(dir, "alpha", "A"); err != nil {
		t.Fatalf("Save alpha: %v", err)
	}

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("List() = %+v, want [alpha zeta] sorted", got)
	}
	if got[0].Text != "A" || got[1].Text != "Z" {
		t.Fatalf("List() text = %+v", got)
	}
}

func TestListOnMissingDirReturnsEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List(missing dir): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List(missing dir) = %+v, want empty", got)
	}
}

func TestDeleteRejectsDefaultPersona(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureDefault(dir); err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if err := Delete(dir, DefaultName); err == nil {
		t.Fatal("Delete(default) should be rejected")
	}
	if got := Load(dir, DefaultName); got != fallbackText {
		t.Fatalf("default persona should be untouched, got %q", got)
	}
}

func TestDeleteRemovesPersonaFile(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "temp", "临时人格"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Delete(dir, "temp"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := Load(dir, "temp"); got != fallbackText {
		t.Fatalf("Load after delete = %q, want fallbackText", got)
	}
}

func TestDeleteMissingPersonaIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if err := Delete(dir, "never-existed"); err != nil {
		t.Fatalf("Delete(never-existed) = %v, want nil", err)
	}
}
