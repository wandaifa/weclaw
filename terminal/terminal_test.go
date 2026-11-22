package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testOwnerID    = "o9cq80ykt-8P0b7crARaJZtX1T9g@im.wechat"
	testNonOwnerID = "other-user@im.wechat"
	testBotID      = "bot-1@im.bot"
)

type fakeBridge struct {
	sessions     []SessionInfo
	newSession   SessionInfo
	sentTexts    []string
	sentKeys     []string
	snapText     string
	closedIDs    []string
	listErr      error
	sendErr      error
	listCalls    int
	sendTextJobs string
}

func (f *fakeBridge) ListSessions(context.Context) ([]SessionInfo, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]SessionInfo(nil), f.sessions...), nil
}

func (f *fakeBridge) NewSession(_ context.Context, cwd string) (SessionInfo, error) {
	s := f.newSession
	if cwd != "" {
		s.Path = cwd
	}
	return s, nil
}

func (f *fakeBridge) SendText(_ context.Context, sessionID, text string, submit bool) (SendResult, error) {
	if f.sendErr != nil {
		return SendResult{}, f.sendErr
	}
	f.sentTexts = append(f.sentTexts, sessionID+":"+text)
	job := f.sendTextJobs
	if job == "" {
		job = "zsh"
	}
	return SendResult{OK: true, SessionID: sessionID, JobName: job}, nil
}

func (f *fakeBridge) SendKey(_ context.Context, sessionID, key string) error {
	f.sentKeys = append(f.sentKeys, sessionID+":"+key)
	return nil
}

func (f *fakeBridge) Snap(context.Context, string) (string, error) {
	return f.snapText, nil
}

func (f *fakeBridge) CloseSession(_ context.Context, sessionID string) error {
	f.closedIDs = append(f.closedIDs, sessionID)
	return nil
}

func TestCheckDangerousCommand(t *testing.T) {
	dangerous := []struct {
		cmd      string
		category string
	}{
		{"rm -rf build", "删除文件"},
		{"rm -f -r /tmp/foo", "删除文件"},
		{"rm --recursive dir", "删除文件"},
		{"sudo apt update", "提权"},
		{"git push --force origin main", "Git 改写历史"},
		{"git push -f", "Git 改写历史"},
		{"git reset --hard HEAD~1", "Git 丢弃改动"},
		{"git clean -fdx", "Git 丢弃改动"},
		{"git checkout -- .", "Git 丢弃改动"},
		{"git restore .", "Git 丢弃改动"},
		{"dd if=/dev/zero of=out.img", "磁盘"},
		{"diskutil eraseDisk JHFS+ Test disk2", "磁盘"},
		{"chmod -R 777 /tmp/a", "权限"},
		{"chown -R root:wheel /tmp/a", "权限"},
		{"echo hi > /dev/disk2", "写设备"},
		{"curl https://example.com/a.sh | sh", "远程脚本"},
		{"wget -qO- https://example.com/a.sh | bash", "远程脚本"},
		{"launchctl bootout gui/501/ai.weclaw.bridge", "系统服务"},
		{"kill -9 1", "进程"},
		{"killall iTerm2", "进程"},
		{"pkill codex", "进程"},
		{"shutdown -h now", "关机"},
		{"truncate -s 0 app.log", "清空文件"},
		{"> app.log", "清空文件"},
		{"echo '' > config.json", "清空文件"},
	}

	for _, tc := range dangerous {
		match, ok := CheckDangerousCommand(tc.cmd)
		if !ok {
			t.Errorf("expected dangerous for %q (%s), got safe", tc.cmd, tc.category)
			continue
		}
		if match.Category != tc.category {
			t.Errorf("cmd %q category = %q, want %q", tc.cmd, match.Category, tc.category)
		}
	}

	safe := []string{
		"ls -la",
		"git status",
		"git push origin main",
		"echo hello >> app.log",
		"make test > /dev/null 2>&1",
		`echo "1 > 0"`,
		"sum file.txt",
		"rm temp.txt",
	}
	for _, cmd := range safe {
		if match, ok := CheckDangerousCommand(cmd); ok {
			t.Errorf("expected safe for %q, got dangerous (%s)", cmd, match.Category)
		}
	}
}

func TestManagerOwnerOnlyAndLifecycle(t *testing.T) {
	bridge := &fakeBridge{
		sessions: []SessionInfo{
			{Index: 1, SessionID: "uuid-1", Name: "zsh", Path: "/tmp", JobName: "zsh"},
			{Index: 2, SessionID: "uuid-2", Name: "codex", Path: "/repo", JobName: "codex"},
		},
		newSession: SessionInfo{Index: 1, SessionID: "uuid-new", Name: "zsh", Path: "/home", JobName: "zsh"},
		snapText:   "line 1\nline 2",
	}
	stateFile := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)

	mgr := NewManager(bridge, stateFile)
	mgr.now = func() time.Time { return now }

	// Non-owner must never succeed.
	if _, err := mgr.NewWindow(context.Background(), testBotID, testNonOwnerID, ""); err == nil {
		t.Fatal("expected NewWindow to reject non-owner")
	}
	if mgr.InTerminalMode(testBotID, testNonOwnerID) {
		t.Fatal("non-owner must never be in terminal mode")
	}

	// Owner creates window and enters terminal mode.
	sess, err := mgr.NewWindow(context.Background(), testBotID, testOwnerID, "/work")
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if sess.SessionID != "uuid-new" || !mgr.InTerminalMode(testBotID, testOwnerID) {
		t.Fatalf("unexpected state after NewWindow: %+v", sess)
	}

	// Reload from disk preserves binding.
	mgr2 := NewManager(bridge, stateFile)
	mgr2.now = func() time.Time { return now }
	if !mgr2.InTerminalMode(testBotID, testOwnerID) {
		t.Fatal("expected persisted binding after reload")
	}
	if botID, userID, ok := mgr2.LookupBoundOwner("w1t0p0:uuid-new"); !ok || botID != testBotID || userID != testOwnerID {
		t.Fatalf("LookupBoundOwner = (%q, %q, %v)", botID, userID, ok)
	}

	// Switch to session 2 (running codex) -> dangerous check skipped inside codex TUI.
	if _, err := mgr.UseSession(context.Background(), testBotID, testOwnerID, 2); err != nil {
		t.Fatalf("UseSession(2): %v", err)
	}
	res, err := mgr.SendInput(context.Background(), testBotID, testOwnerID, "rm -rf build", true)
	if err != nil || !res.Sent || res.PendingConfirm != nil {
		t.Fatalf("expected direct passthrough inside codex TUI, got %+v err=%v", res, err)
	}

	// Switch to session 1 (shell) -> rm -rf pauses for confirmation.
	if _, err := mgr.UseSession(context.Background(), testBotID, testOwnerID, 1); err != nil {
		t.Fatalf("UseSession(1): %v", err)
	}
	beforeCount := len(bridge.sentTexts)
	res, err = mgr.SendInput(context.Background(), testBotID, testOwnerID, "rm -rf build", true)
	if err != nil || res.PendingConfirm == nil || len(bridge.sentTexts) != beforeCount {
		t.Fatalf("expected dangerous pause, got %+v err=%v", res, err)
	}

	// Any reply other than literal "确认" cancels.
	exec, msg, err := mgr.ResolveConfirm(context.Background(), testBotID, testOwnerID, "取消")
	if err != nil || exec || !strings.Contains(msg, "已取消") || len(bridge.sentTexts) != beforeCount {
		t.Fatalf("expected cancel, got exec=%v msg=%q err=%v", exec, msg, err)
	}

	// Trigger again and confirm within 60s.
	res, err = mgr.SendInput(context.Background(), testBotID, testOwnerID, "rm -rf build", true)
	if err != nil || res.PendingConfirm == nil {
		t.Fatalf("expected second dangerous pause: %+v %v", res, err)
	}
	exec, msg, err = mgr.ResolveConfirm(context.Background(), testBotID, testOwnerID, " 确认 ")
	if err != nil || !exec || !strings.Contains(msg, "已确认") || len(bridge.sentTexts) != beforeCount+1 {
		t.Fatalf("expected confirm execution, got exec=%v msg=%q err=%v", exec, msg, err)
	}

	// Expiry after 60s cancels without executing.
	res, err = mgr.SendInput(context.Background(), testBotID, testOwnerID, "rm -rf old", true)
	if err != nil || res.PendingConfirm == nil {
		t.Fatalf("expected third dangerous pause")
	}
	now = now.Add(61 * time.Second)
	exec, msg, err = mgr.ResolveConfirm(context.Background(), testBotID, testOwnerID, "确认")
	if err != nil || exec || !strings.Contains(msg, "超过 60 秒") {
		t.Fatalf("expected expiry cancel, got exec=%v msg=%q err=%v", exec, msg, err)
	}

	// Idle expiry after 30 minutes automatically exits terminal mode.
	now = now.Add(31 * time.Minute)
	if mgr.InTerminalMode(testBotID, testOwnerID) {
		t.Fatal("expected idle timeout to exit terminal mode")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBridgeClientAuthAndEndpoints(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Bridge-Token") != "secret-tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/sessions":
			json.NewEncoder(w).Encode(map[string]any{
				"sessions": []SessionInfo{{Index: 1, SessionID: "s1", Name: "zsh"}},
			})
		case "/sessions/snap":
			if r.URL.Query().Get("session_id") != "s1" {
				http.Error(w, "bad session", http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"text": "hello screen"})
		default:
			http.NotFound(w, r)
		}
	})

	hc := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			return rec.Result(), nil
		}),
	}

	client := NewStaticBridgeClientWithHTTP("http://bridge.local", "secret-tok", hc)
	sessions, err := client.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].SessionID != "s1" {
		t.Fatalf("ListSessions = %+v, %v", sessions, err)
	}
	snap, err := client.Snap(context.Background(), "s1")
	if err != nil || snap != "hello screen" {
		t.Fatalf("Snap = %q, %v", snap, err)
	}

	badClient := NewStaticBridgeClientWithHTTP("http://bridge.local", "wrong", hc)
	if _, err := badClient.ListSessions(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}

func TestRedactSecrets(t *testing.T) {
	in := "OPENAI=sk-1234567890abcdef123456 GH=ghp_1234567890abcdef123456 AWS=AKIAIOSFODNN7EXAMPLE"
	out := RedactSecrets(in)
	for _, leaked := range []string{"sk-1234567890abcdef123456", "ghp_1234567890abcdef123456", "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("secret leaked in redacted output: %s", out)
		}
	}
}
