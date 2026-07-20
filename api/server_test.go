package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
