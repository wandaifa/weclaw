package messaging

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestDeferredLoginWelcomeUsesInboundContextAndSendsOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	owner := config.OwnerUserIDs()[0]
	var got []ilink.SendMessageRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ilink.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		got = append(got, req)
		json.NewEncoder(w).Encode(map[string]int{"ret": 0})
	}))
	defer srv.Close()
	if err := ilink.MarkLoginWelcomePending("welcome@im.bot", owner); err != nil {
		t.Fatal(err)
	}
	// A freshly created client simulates loading the account after restart.
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "welcome@im.bot", BaseURL: srv.URL})
	if len(got) != 0 {
		t.Fatal("login must not send any message")
	}
	if err := sendPendingLoginWelcome(context.Background(), client, ilink.WeixinMessage{FromUserID: owner}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("missing inbound context must not send")
	}
	if err := sendPendingLoginWelcome(context.Background(), client, ilink.WeixinMessage{FromUserID: "other", ContextToken: "ctx"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("other user must not consume welcome")
	}
	msg := ilink.WeixinMessage{FromUserID: owner, ContextToken: "inbound-context"}
	for i := 0; i < 2; i++ {
		if err := sendPendingLoginWelcome(context.Background(), client, msg); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 || got[0].Msg.ContextToken != "inbound-context" || got[0].Msg.ToUserID != owner {
		t.Fatalf("unexpected requests: %#v", got)
	}
	if got[0].Msg.ItemList[0].TextItem.Text != LoginWelcomeText {
		t.Fatal("wrong welcome text")
	}
	user, err := ilink.PendingLoginWelcomeUser(client.BotID())
	if err != nil || user != "" {
		t.Fatalf("welcome not completed: %q %v", user, err)
	}
}

func TestDeferredLoginWelcomeFailureRetainsPending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	owner := config.OwnerUserIDs()[0]
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			json.NewEncoder(w).Encode(map[string]interface{}{"ret": -2, "errmsg": "prepare failed"})
			return
		}
		json.NewEncoder(w).Encode(map[string]int{"ret": 0})
	}))
	defer srv.Close()
	if err := ilink.MarkLoginWelcomePending("welcome@im.bot", owner); err != nil {
		t.Fatal(err)
	}
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "welcome@im.bot", BaseURL: srv.URL})
	msg := ilink.WeixinMessage{FromUserID: owner, ContextToken: "ctx"}
	if err := sendPendingLoginWelcome(context.Background(), client, msg); err == nil {
		t.Fatal("expected failure")
	}
	if calls != 1 {
		t.Fatal("must not retry continuously")
	}
	user, err := ilink.PendingLoginWelcomeUser(client.BotID())
	if err != nil || user != owner {
		t.Fatal("failure consumed pending welcome")
	}
	if err := sendPendingLoginWelcome(context.Background(), client, msg); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("next message should retry once")
	}
}

func TestDeferredLoginWelcomeDoesNotWelcomeLegacyAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "legacy@im.bot", BaseURL: "http://127.0.0.1:1"})
	if err := sendPendingLoginWelcome(context.Background(), client, ilink.WeixinMessage{FromUserID: config.OwnerUserIDs()[0], ContextToken: "ctx"}); err != nil {
		t.Fatal(err)
	}
}

func TestWelcomeFirstMessageStillReachesAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	owner := config.OwnerUserIDs()[0]
	srv, sent := newFakeIlinkServer(t)
	defer srv.Close()
	if err := ilink.MarkLoginWelcomePending("bot1@im.bot", owner); err != nil {
		t.Fatal(err)
	}
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	h := newTestHandler()
	fake := &recordingAgent{}
	h.SetDefaultAgent("codex", fake)
	msg := newTestMessage(owner, "hello", 9001)
	msg.ContextToken = "ctx"
	h.HandleMessage(context.Background(), client, msg)
	h.waitForIdle()
	if len(fake.callsSnapshot()) != 1 {
		t.Fatal("first message did not reach agent")
	}
	replies := sent.snapshot()
	if len(replies) < 2 || replies[0] != LoginWelcomeText || replies[len(replies)-1] != "ok" {
		t.Fatalf("replies: %v", replies)
	}
	h.HandleMessage(context.Background(), client, msg)
	h.waitForIdle()
	if len(sent.snapshot()) != len(replies) {
		t.Fatal("duplicate message sent welcome again")
	}
}

func TestDeniedScannerDoesNotReceiveWelcome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, sent := newFakeIlinkServer(t)
	defer srv.Close()
	scanner := "denied-scanner@im.wechat"
	if err := ilink.MarkLoginWelcomePending("bot1@im.bot", scanner); err != nil {
		t.Fatal(err)
	}
	client := ilink.NewClient(&ilink.Credentials{ILinkBotID: "bot1@im.bot", BaseURL: srv.URL})
	h := newTestHandler()
	h.SetAccessMode("owner_only")
	msg := newTestMessage(scanner, "hello", 9101)
	msg.ContextToken = "ctx"
	h.HandleMessage(context.Background(), client, msg)
	h.waitForIdle()
	for _, reply := range sent.snapshot() {
		if reply == LoginWelcomeText {
			t.Fatal("access-denied scanner received welcome")
		}
	}
	user, err := ilink.PendingLoginWelcomeUser(client.BotID())
	if err != nil || user != scanner {
		t.Fatal("access gate consumed pending welcome")
	}
}

// Opt-in real send; recipient is always an allowlisted owner. Welcome state
// lives in a temporary HOME, so production accounts are never marked pending.
func TestOwnerDeferredLoginWelcomeLive(t *testing.T) {
	if os.Getenv("WECLAW_TEST_OWNER_WELCOME") != "1" {
		t.Skip("explicit owner-only live test")
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(realHome, ".weclaw", "weclaw.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var botID, userID, contextToken, inboundTime string
	for _, owner := range config.OwnerUserIDs() {
		err = db.QueryRow("SELECT bot_id,user_id,context_token,time FROM messages WHERE role='user' AND user_id=? AND context_token!='' ORDER BY id DESC LIMIT 1", owner).Scan(&botID, &userID, &contextToken, &inboundTime)
		if err == nil {
			break
		}
	}
	if err != nil || !config.IsOwner(userID) {
		t.Fatal("no verified owner context")
	}
	at, err := time.Parse(time.RFC3339, inboundTime)
	if err != nil || time.Since(at) > 24*time.Hour {
		t.Fatal("owner context expired; owner must send a new message first")
	}
	data, err := os.ReadFile(filepath.Join(realHome, ".weclaw", "accounts", ilink.NormalizeAccountID(botID)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var creds ilink.Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatal(err)
	}
	if creds.ILinkBotID != botID || !config.IsOwner(creds.ILinkUserID) {
		t.Fatal("refusing non-owner account")
	}
	t.Setenv("HOME", t.TempDir())
	if err := ilink.MarkLoginWelcomePending(botID, userID); err != nil {
		t.Fatal(err)
	}
	client := ilink.NewClient(&creds)
	msg := ilink.WeixinMessage{FromUserID: userID, ContextToken: contextToken}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := sendPendingLoginWelcome(ctx, client, msg); err != nil {
		t.Fatal(err)
	}
	user, err := ilink.PendingLoginWelcomeUser(botID)
	if err != nil || user != "" {
		t.Fatal("real welcome did not complete state")
	}
	if err := sendPendingLoginWelcome(ctx, client, msg); err != nil {
		t.Fatal(err)
	}
	t.Log("owner-only real welcome succeeded; completed state prevented a second send")
}
