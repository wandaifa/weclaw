package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectPermissionOption(t *testing.T) {
	options := []permissionOption{
		{OptionID: "opt-allow-once", Kind: "allow_once"},
		{OptionID: "opt-allow-always", Kind: "allow_always"},
		{OptionID: "opt-reject-once", Kind: "reject_once"},
		{OptionID: "opt-reject-always", Kind: "reject_always"},
	}
	if got := selectPermissionOption(options, true); got != "opt-allow-once" {
		t.Errorf("allow -> %q, want opt-allow-once", got)
	}
	if got := selectPermissionOption(options, false); got != "opt-reject-once" {
		t.Errorf("deny -> %q, want opt-reject-once", got)
	}

	// No "_once" variant offered: fall back to any option of the right family.
	onlyAlways := []permissionOption{
		{OptionID: "a", Kind: "allow_always"},
		{OptionID: "r", Kind: "reject_always"},
	}
	if got := selectPermissionOption(onlyAlways, true); got != "a" {
		t.Errorf("allow fallback -> %q, want a", got)
	}
	if got := selectPermissionOption(onlyAlways, false); got != "r" {
		t.Errorf("deny fallback -> %q, want r", got)
	}

	// Nothing matches the requested family: fall back to the first option so we always answer.
	if got := selectPermissionOption(onlyAlways, false); got != "r" {
		t.Errorf("deny with only allow options should still return something, got %q", got)
	}
	if got := selectPermissionOption(nil, true); got != "" {
		t.Errorf("no options -> %q, want empty", got)
	}
}

type fakeWriteCloser struct{ bytes.Buffer }

func (f *fakeWriteCloser) Close() error { return nil }

func TestHandlePermissionRequestDecidesByPolicy(t *testing.T) {
	rawRequest := func(sessionID string) string {
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "session/request_permission",
			"params": map[string]interface{}{
				"sessionId": sessionID,
				"options": []map[string]string{
					{"optionId": "opt-allow-once", "kind": "allow_once"},
					{"optionId": "opt-reject-once", "kind": "reject_once"},
				},
			},
		}
		data, _ := json.Marshal(req)
		return string(data)
	}

	t.Run("owner is allowed", func(t *testing.T) {
		a := NewACPAgent(ACPAgentConfig{})
		out := &fakeWriteCloser{}
		a.stdin = out
		a.sessionOwners["sess-owner"] = "owner-conv"
		a.policies["owner-conv"] = ConversationPolicy{Level: SandboxFullAccess}

		a.handlePermissionRequest(rawRequest("sess-owner"))

		if !strings.Contains(out.String(), `"optionId":"opt-allow-once"`) {
			t.Fatalf("expected owner to be allowed, response: %s", out.String())
		}
	})

	t.Run("non-owner is denied", func(t *testing.T) {
		a := NewACPAgent(ACPAgentConfig{})
		out := &fakeWriteCloser{}
		a.stdin = out
		a.sessionOwners["sess-other"] = "someone-else"
		a.policies["someone-else"] = ConversationPolicy{Level: SandboxReadOnly}

		a.handlePermissionRequest(rawRequest("sess-other"))

		if !strings.Contains(out.String(), `"optionId":"opt-reject-once"`) {
			t.Fatalf("expected non-owner to be denied, response: %s", out.String())
		}
	})

	t.Run("unknown session fails closed (denied)", func(t *testing.T) {
		a := NewACPAgent(ACPAgentConfig{})
		out := &fakeWriteCloser{}
		a.stdin = out
		// No sessionOwners/policies entry at all for this session.

		a.handlePermissionRequest(rawRequest("sess-unmapped"))

		if !strings.Contains(out.String(), `"optionId":"opt-reject-once"`) {
			t.Fatalf("expected unmapped session to fail closed (denied), response: %s", out.String())
		}
	})
}

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
