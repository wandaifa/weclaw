package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/api"
	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestReloadAccountsAt(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != api.AccountReloadPath {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	running, err := reloadAccountsAt(context.Background(), strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if !running || !called {
		t.Fatalf("running = %v, called = %v, want both true", running, called)
	}
}

func TestLocalAPIURLNormalizesWildcardHost(t *testing.T) {
	got, err := localAPIURL("0.0.0.0:18011", api.AccountReloadPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:18011" + api.AccountReloadPath
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

// TestSendLoginWelcomeWithRetryRetriesUntilSuccess is a regression test for
// a real failure observed 2026-07-24: sending the login welcome message
// immediately after QR confirmation can hit ret=-2 "prepare failed" because
// the just-confirmed account isn't ready to receive send requests on
// iLink's backend yet, even though credentials are already valid. Retrying
// a few times with a short delay gives that window time to pass.
func TestSendLoginWelcomeWithRetryRetriesUntilSuccess(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			json.NewEncoder(w).Encode(map[string]interface{}{"ret": -2, "errmsg": "prepare failed"})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0})
	}))
	defer server.Close()

	creds := &ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", ILinkUserID: "user1@im.wechat", BaseURL: server.URL}
	if err := sendLoginWelcomeWithRetry(context.Background(), creds, 5, 10*time.Millisecond); err != nil {
		t.Fatalf("sendLoginWelcomeWithRetry: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3 (2 failures then a success)", got)
	}
}

func TestSendLoginWelcomeWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": -2, "errmsg": "prepare failed"})
	}))
	defer server.Close()

	creds := &ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", ILinkUserID: "user1@im.wechat", BaseURL: server.URL}
	err := sendLoginWelcomeWithRetry(context.Background(), creds, 3, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error after exhausting all retry attempts")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3 (maxAttempts, no retry beyond that)", got)
	}
}

func TestSendLoginWelcomeWithRetryRequiresScannerUserID(t *testing.T) {
	creds := &ilink.Credentials{BotToken: "tok", ILinkBotID: "bot1@im.bot", BaseURL: "http://127.0.0.1:1"}
	if err := sendLoginWelcomeWithRetry(context.Background(), creds, 3, 10*time.Millisecond); err == nil {
		t.Fatal("expected an error when credentials have no ILinkUserID (scanner identity unknown)")
	}
}
