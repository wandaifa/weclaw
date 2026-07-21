package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexSandboxParams(t *testing.T) {
	cases := []struct {
		name      string
		policy    ConversationPolicy
		agentCwd  string
		wantMode  string
		wantType  string
		wantCwd   string
		wantRoots []string
	}{
		{
			name:     "read only defaults to agent cwd",
			policy:   ConversationPolicy{Level: SandboxReadOnly},
			agentCwd: "/agent/cwd",
			wantMode: "read-only", wantType: "readOnly", wantCwd: "/agent/cwd",
		},
		{
			name:     "workspace write uses policy cwd and writable roots",
			policy:   ConversationPolicy{Level: SandboxWorkspaceWrite, Cwd: "/sandbox/user1", WritableRoots: []string{"/sandbox/user1"}},
			agentCwd: "/agent/cwd",
			wantMode: "workspace-write", wantType: "workspaceWrite", wantCwd: "/sandbox/user1",
			wantRoots: []string{"/sandbox/user1"},
		},
		{
			name:     "full access is unrestricted",
			policy:   ConversationPolicy{Level: SandboxFullAccess},
			agentCwd: "/agent/cwd",
			wantMode: "danger-full-access", wantType: "dangerFullAccess", wantCwd: "/agent/cwd",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, policy, cwd := codexSandboxParams(tc.policy, tc.agentCwd)
			if mode != tc.wantMode {
				t.Errorf("sandbox mode = %q, want %q", mode, tc.wantMode)
			}
			if policy["type"] != tc.wantType {
				t.Errorf("sandboxPolicy type = %v, want %q", policy["type"], tc.wantType)
			}
			if cwd != tc.wantCwd {
				t.Errorf("cwd = %q, want %q", cwd, tc.wantCwd)
			}
			if tc.wantRoots != nil {
				roots, _ := policy["writableRoots"].([]string)
				if len(roots) != len(tc.wantRoots) || roots[0] != tc.wantRoots[0] {
					t.Errorf("writableRoots = %v, want %v", roots, tc.wantRoots)
				}
			}
		})
	}
}

func TestGetOrCreateThreadAppliesConversationPolicy(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Args: []string{"app-server"}, Cwd: "/agent/cwd"})

	var calls int
	var lastParams map[string]interface{}
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		calls++
		if method != "thread/start" {
			t.Fatalf("unexpected method %q", method)
		}
		lastParams = params.(map[string]interface{})
		return json.RawMessage(`{"thread":{"id":"thread-` + string(rune('0'+calls)) + `"}}`), nil
	}

	// No SetConversationPolicy call yet -> fail-closed default: read-only.
	if _, _, err := a.getOrCreateThread(context.Background(), "conv1"); err != nil {
		t.Fatalf("getOrCreateThread: %v", err)
	}
	if lastParams["sandbox"] != "read-only" {
		t.Fatalf("default sandbox = %v, want read-only", lastParams["sandbox"])
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	// Same conversation, no policy change -> cached thread, no new RPC call.
	if _, isNew, err := a.getOrCreateThread(context.Background(), "conv1"); err != nil || isNew {
		t.Fatalf("expected cached thread, isNew=%v err=%v", isNew, err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (thread should be cached)", calls)
	}

	// Upgrading the policy invalidates the cached thread and rebuilds with new params.
	dir := t.TempDir()
	a.SetConversationPolicy("conv1", ConversationPolicy{Level: SandboxWorkspaceWrite, Cwd: dir, WritableRoots: []string{dir}})
	if _, isNew, err := a.getOrCreateThread(context.Background(), "conv1"); err != nil || !isNew {
		t.Fatalf("expected new thread after policy change, isNew=%v err=%v", isNew, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 after policy change", calls)
	}
	if lastParams["sandbox"] != "workspace-write" || lastParams["cwd"] != dir {
		t.Fatalf("params after upgrade = %v, want workspace-write @ %s", lastParams, dir)
	}

	// Setting the identical policy again must not invalidate the thread.
	a.SetConversationPolicy("conv1", ConversationPolicy{Level: SandboxWorkspaceWrite, Cwd: dir, WritableRoots: []string{dir}})
	if _, isNew, err := a.getOrCreateThread(context.Background(), "conv1"); err != nil || isNew {
		t.Fatalf("expected cached thread after identical policy, isNew=%v err=%v", isNew, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want still 2 (no-op policy set)", calls)
	}

	// A different conversation defaults independently to read-only.
	if _, _, err := a.getOrCreateThread(context.Background(), "conv2"); err != nil {
		t.Fatalf("getOrCreateThread conv2: %v", err)
	}
	if lastParams["sandbox"] != "read-only" {
		t.Fatalf("conv2 sandbox = %v, want read-only", lastParams["sandbox"])
	}
}

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
