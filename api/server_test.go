package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
)

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
