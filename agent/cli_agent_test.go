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

func TestBuildClaudeArgsNonOwnerOverrideEnablesSafeMode(t *testing.T) {
	override := PersonaOverride{SafeMode: true, SystemPrompt: "你是通用助手"}
	got := buildClaudeArgs("hi", "", "", nil, override, "", false)
	want := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--safe-mode", "--append-system-prompt", "你是通用助手"}
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

func TestCLIAgentSetModelConfigUpdatesModelUsedByNextSpawn(t *testing.T) {
	a := NewCLIAgent(CLIAgentConfig{Name: "claude", Command: "claude", Model: "sonnet"})

	// reasoningEffort is accepted (satisfies agent.ModelConfigurableAgent) but
	// claude's CLI has no reasoning-effort concept, so it's just ignored.
	a.SetModelConfig("opus", "high")

	a.mu.Lock()
	got := a.model
	a.mu.Unlock()
	if got != "opus" {
		t.Fatalf("model = %q, want opus", got)
	}
}
