package agent

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
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

func TestBuildClaudeImageArgsOwnerNoOverride(t *testing.T) {
	got := buildClaudeImageArgs("sonnet", "", nil, PersonaOverride{}, "", false)
	want := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--model", "sonnet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeImageArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeImageArgsNonOwnerOverrideEnablesSafeMode(t *testing.T) {
	override := PersonaOverride{SafeMode: true, SystemPrompt: "你是通用助手"}
	got := buildClaudeImageArgs("", "", nil, override, "", false)
	want := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--safe-mode", "--append-system-prompt", "你是通用助手"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeImageArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeImageArgsResumesSession(t *testing.T) {
	got := buildClaudeImageArgs("", "", nil, PersonaOverride{}, "sess-123", true)
	want := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--resume", "sess-123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildClaudeImageArgs() = %#v, want %#v", got, want)
	}
}

func TestBuildClaudeImageStdinIncludesTextAndBase64Image(t *testing.T) {
	got, err := buildClaudeImageStdin("请识别这张图片", &ImageInput{MimeType: "image/png", Data: []byte("fake-png-bytes")})
	if err != nil {
		t.Fatalf("buildClaudeImageStdin: %v", err)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("stdin payload should end with a newline (one NDJSON line), got %q", got)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(got)), &decoded); err != nil {
		t.Fatalf("stdin payload is not valid JSON: %v (payload=%q)", err, got)
	}
	if decoded["type"] != "user" {
		t.Fatalf("top-level type = %v, want \"user\" (matches the shape empirically verified against a real claude process on 2026-07-24)", decoded["type"])
	}
	message, ok := decoded["message"].(map[string]interface{})
	if !ok || message["role"] != "user" {
		t.Fatalf("message.role = %v, want \"user\"", message["role"])
	}
	content, ok := message["content"].([]interface{})
	if !ok || len(content) != 2 {
		t.Fatalf("message.content = %v, want a 2-element array (text block + image block)", message["content"])
	}
	textBlock := content[0].(map[string]interface{})
	if textBlock["type"] != "text" || textBlock["text"] != "请识别这张图片" {
		t.Fatalf("content[0] = %+v, want text block with the given message", textBlock)
	}
	imageBlock := content[1].(map[string]interface{})
	if imageBlock["type"] != "image" {
		t.Fatalf("content[1].type = %v, want \"image\"", imageBlock["type"])
	}
	source, ok := imageBlock["source"].(map[string]interface{})
	if !ok || source["type"] != "base64" || source["media_type"] != "image/png" {
		t.Fatalf("content[1].source = %+v, want base64/image/png", imageBlock["source"])
	}
	wantData := base64.StdEncoding.EncodeToString([]byte("fake-png-bytes"))
	if source["data"] != wantData {
		t.Fatalf("content[1].source.data = %v, want %v", source["data"], wantData)
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
