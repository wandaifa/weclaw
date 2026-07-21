package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestHandleSendRequiresBotIDForMultipleAccounts(t *testing.T) {
	server := NewServer([]*ilink.Client{
		ilink.NewClient(&ilink.Credentials{ILinkBotID: "first@im.bot"}),
		ilink.NewClient(&ilink.Credentials{ILinkBotID: "second@im.bot"}),
	}, "")

	req := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewBufferString(`{"to":"user@im.wechat","text":"hello"}`))
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleSendRejectsUnknownBotID(t *testing.T) {
	server := NewServer([]*ilink.Client{
		ilink.NewClient(&ilink.Credentials{ILinkBotID: "known@im.bot"}),
	}, "")

	req := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewBufferString(`{"bot_id":"missing@im.bot","to":"user@im.wechat","text":"hello"}`))
	rec := httptest.NewRecorder()
	server.handleSend(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAccountReloadUpdatesClients(t *testing.T) {
	server := NewServer(nil, "")
	server.SetAccountReloader(func(context.Context) (AccountReloadResult, error) {
		client := ilink.NewClient(&ilink.Credentials{
			ILinkBotID: "bot-1",
			BotToken:   "token-1",
		})
		return AccountReloadResult{Clients: []*ilink.Client{client}, Added: 1}, nil
	})

	req := httptest.NewRequest(http.MethodPost, AccountReloadPath, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	server.handleAccountReload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := len(server.clientsSnapshot()); got != 1 {
		t.Fatalf("client count = %d, want 1", got)
	}
}

func TestHandleAccountReloadRejectsNonLoopback(t *testing.T) {
	called := false
	server := NewServer(nil, "")
	server.SetAccountReloader(func(context.Context) (AccountReloadResult, error) {
		called = true
		return AccountReloadResult{}, nil
	})

	req := httptest.NewRequest(http.MethodPost, AccountReloadPath, nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rec := httptest.NewRecorder()
	server.handleAccountReload(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if called {
		t.Fatal("reloader should not be called for a non-loopback request")
	}
}

func TestHandleAccountsReturnsLoadedBotIDs(t *testing.T) {
	server := NewServer([]*ilink.Client{
		ilink.NewClient(&ilink.Credentials{ILinkBotID: "first@im.bot"}),
		ilink.NewClient(&ilink.Credentials{ILinkBotID: "second@im.bot"}),
	}, "")
	req := httptest.NewRequest(http.MethodGet, AccountsPath, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handleAccounts(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	var body struct {
		Accounts []AccountStatus `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Accounts) != 2 || body.Accounts[0].BotID != "first@im.bot" || body.Accounts[1].BotID != "second@im.bot" {
		t.Fatalf("accounts = %v", body.Accounts)
	}
}

func TestHandleAccountStateDelegatesToController(t *testing.T) {
	called := false
	server := NewServer(nil, "")
	server.SetAccountStateController(func(_ context.Context, botID string, disabled bool) (AccountReloadResult, error) {
		called = botID == "bot@im.bot" && disabled
		return AccountReloadResult{Clients: []*ilink.Client{ilink.NewClient(&ilink.Credentials{ILinkBotID: "other@im.bot"})}}, nil
	})
	req := httptest.NewRequest(http.MethodPost, AccountStatePath, bytes.NewBufferString(`{"bot_id":"bot@im.bot","disabled":true}`))
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handleAccountState(resp, req)
	if resp.Code != http.StatusOK || !called {
		t.Fatalf("status = %d, called = %v: %s", resp.Code, called, resp.Body.String())
	}
	if got := len(server.clientsSnapshot()); got != 1 {
		t.Fatalf("client count = %d, want 1", got)
	}
}

func TestHandleMessageMergeReadsAndUpdatesSettings(t *testing.T) {
	current := MessageMergeSettings{IdleSeconds: 3, MaxWaitSeconds: 10, MaxMessages: 10, MaxChars: 4000}
	server := NewServer(nil, "")
	server.SetMessageMergeProvider(func() MessageMergeSettings { return current })
	server.SetMessageMergeController(func(_ context.Context, settings MessageMergeSettings) (MessageMergeSettings, error) {
		current = settings
		return current, nil
	})

	get := httptest.NewRequest(http.MethodGet, MessageMergePath, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResp := httptest.NewRecorder()
	server.handleMessageMerge(getResp, get)
	if getResp.Code != http.StatusOK || !strings.Contains(getResp.Body.String(), `"idle_seconds":3`) {
		t.Fatalf("GET = %d %s", getResp.Code, getResp.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, MessageMergePath, bytes.NewBufferString(`{"idle_seconds":4,"max_wait_seconds":12,"max_messages":8,"max_chars":3000}`))
	post.RemoteAddr = "127.0.0.1:12345"
	postResp := httptest.NewRecorder()
	server.handleMessageMerge(postResp, post)
	if postResp.Code != http.StatusOK || current.IdleSeconds != 4 || current.MaxChars != 3000 {
		t.Fatalf("POST = %d settings=%+v", postResp.Code, current)
	}
}

func TestHandlePermissionsReadsAndUpdates(t *testing.T) {
	stored := map[string]UserPermissionInfo{
		"owner@im.wechat": {UserID: "owner@im.wechat", Level: "full_access", IsOwner: true},
	}
	server := NewServer(nil, "")
	server.SetPermissionsProvider(func() []UserPermissionInfo {
		out := make([]UserPermissionInfo, 0, len(stored))
		for _, v := range stored {
			out = append(out, v)
		}
		return out
	})
	server.SetPermissionController(func(_ context.Context, req PermissionSetRequest) (UserPermissionInfo, error) {
		info := UserPermissionInfo{UserID: req.UserID, Level: req.Level, DailyLimit: req.DailyLimit}
		stored[req.UserID] = info
		return info, nil
	})

	get := httptest.NewRequest(http.MethodGet, PermissionsPath, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResp := httptest.NewRecorder()
	server.handlePermissions(getResp, get)
	if getResp.Code != http.StatusOK || !strings.Contains(getResp.Body.String(), "full_access") {
		t.Fatalf("GET = %d %s", getResp.Code, getResp.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, PermissionsPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","level":"workspace_write","daily_limit":30}`))
	post.RemoteAddr = "127.0.0.1:12345"
	postResp := httptest.NewRecorder()
	server.handlePermissions(postResp, post)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", postResp.Code, postResp.Body.String())
	}
	if got := stored["someone@im.wechat"]; got.Level != "workspace_write" || got.DailyLimit != 30 {
		t.Fatalf("stored permission = %+v", got)
	}
}

func TestHandlePermissionsRejectsNonLoopback(t *testing.T) {
	called := false
	server := NewServer(nil, "")
	server.SetPermissionsProvider(func() []UserPermissionInfo {
		called = true
		return nil
	})
	req := httptest.NewRequest(http.MethodGet, PermissionsPath, nil)
	req.RemoteAddr = "192.0.2.1:12345"
	resp := httptest.NewRecorder()
	server.handlePermissions(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	if called {
		t.Fatal("provider should not be called for a non-loopback request")
	}
}

func TestHandlePermissionsUsageReturnsSnapshot(t *testing.T) {
	server := NewServer(nil, "")
	server.SetUsageProvider(func() []UsageInfo {
		return []UsageInfo{{UserID: "someone@im.wechat", Date: "2026-07-21", Count: 3, Limit: 50}}
	})
	req := httptest.NewRequest(http.MethodGet, PermissionsUsagePath, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handlePermissionsUsage(resp, req)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"count":3`) {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
}
