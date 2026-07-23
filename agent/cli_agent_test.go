package agent

import (
	"reflect"
	"testing"
)

func TestBuildClaudeArgsOwnerNoOverride(t *testing.T) {
	got := buildClaudeArgs("hi", "sonnet", "", nil, PersonaOverride{}, "", false)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--model", "sonnet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeArgsNonOwnerOverrideExcludesUserSettings(t *testing.T) {
	override := PersonaOverride{SettingSources: "project,local", SystemPrompt: "你是通用助手"}
	got := buildClaudeArgs("hi", "", "", nil, override, "", false)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--setting-sources", "project,local", "--append-system-prompt", "你是通用助手"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeArgsCombinesConfiguredAndOverridePrompt(t *testing.T) {
	override := PersonaOverride{SystemPrompt: "脱敏人格"}
	got := buildClaudeArgs("hi", "", "配置的提示", nil, override, "", false)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--append-system-prompt", "配置的提示\n\n脱敏人格"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeArgsResumesSession(t *testing.T) {
	got := buildClaudeArgs("hi", "", "", []string{"--dangerously-skip-permissions"}, PersonaOverride{}, "sess-123", true)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "--resume", "sess-123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeArgs() = %#v, want %#v", got, want)
	}
}

func TestSetPersonaOverrideIsPerConversation(t *testing.T) {
	a := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude"})
	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "A"})
	a.SetPersonaOverride("user-b", PersonaOverride{SystemPrompt: "B"})

	a.mu.Lock()
	gotA := a.personas["user-a"]
	gotB := a.personas["user-b"]
	a.mu.Unlock()

	if gotA.SystemPrompt != "A" || gotB.SystemPrompt != "B" {
		t.Fatalf("personas not stored independently: a=%+v b=%+v", gotA, gotB)
	}
}
