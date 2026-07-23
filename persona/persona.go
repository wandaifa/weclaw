// Package persona stores the text of each configurable "who is the bot to
// this user" identity, one file per persona, so wx-clawbot's admin panel and
// weclaw's chat handler share a single on-disk source of truth.
package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultName is the persona every non-owner user gets unless explicitly
// bound to a different one. Its file is seeded automatically if missing.
const DefaultName = "default"

// fallbackText is returned by Load when a persona's file is missing or
// unreadable. It carries no owner-specific information, so a storage
// failure can never leak privacy — it just degrades to a generic assistant
// voice.
const fallbackText = "你是一个通用 AI 助手，擅长电商运营、日常问答和文字处理。保持专业、简洁、友善的语气回答问题。不了解的具体人物、公司、私人信息一律如实说不知道，不要编造。"

// Persona is one named system-prompt override.
type Persona struct {
	Name string
	Text string
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Dir returns ~/.weclaw/personas. It does not create the directory.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "personas"), nil
}

func pathFor(dir, name string) (string, error) {
	if !nameRE.MatchString(name) {
		return "", fmt.Errorf("invalid persona name %q: only letters, digits, - and _ are allowed", name)
	}
	return filepath.Join(dir, name+".md"), nil
}

// EnsureDefault creates dir and dir/default.md (seeded with fallbackText) if
// either is missing. Call once at startup before serving any chat. It never
// overwrites an existing default.md, so an admin's customization survives
// restarts.
func EnsureDefault(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create personas dir: %w", err)
	}
	path, err := pathFor(dir, DefaultName)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(fallbackText), 0o600)
}

// List returns every persona file in dir, sorted by name. A missing dir is
// not an error — it just means no personas exist yet.
func List(dir string) ([]Persona, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read personas dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)

	out := make([]Persona, 0, len(names))
	for _, name := range names {
		out = append(out, Persona{Name: name, Text: Load(dir, name)})
	}
	return out, nil
}

// Load reads one persona's text from dir. It never returns an error: a
// missing file, an invalid name, or any read failure all degrade to
// fallbackText, since a storage hiccup must never block a chat turn or leak
// stale content from another persona.
func Load(dir, name string) string {
	path, err := pathFor(dir, name)
	if err != nil {
		return fallbackText
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fallbackText
	}
	return string(data)
}

// Save writes text to dir/<name>.md, creating dir if needed.
func Save(dir, name, text string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create personas dir: %w", err)
	}
	path, err := pathFor(dir, name)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

// Delete removes dir/<name>.md. Deleting DefaultName is rejected: every
// non-owner user can fall back to it, so it must always exist.
func Delete(dir, name string) error {
	if name == DefaultName {
		return fmt.Errorf("cannot delete the %q persona", DefaultName)
	}
	path, err := pathFor(dir, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete persona %q: %w", name, err)
	}
	return nil
}
