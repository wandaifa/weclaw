package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestHandleAccountRemoveDelegatesToController(t *testing.T) {
	called := ""
	server := NewServer(nil, "")
	server.SetAccountRemoveController(func(_ context.Context, botID string) (AccountReloadResult, error) {
		called = botID
		return AccountReloadResult{Clients: []*ilink.Client{ilink.NewClient(&ilink.Credentials{ILinkBotID: "other@im.bot"})}}, nil
	})
	req := httptest.NewRequest(http.MethodPost, AccountRemovePath, bytes.NewBufferString(`{"bot_id":"bot@im.bot"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handleAccountRemove(resp, req)
	if resp.Code != http.StatusOK || called != "bot@im.bot" {
		t.Fatalf("status = %d, called = %q: %s", resp.Code, called, resp.Body.String())
	}
	if got := len(server.clientsSnapshot()); got != 1 {
		t.Fatalf("client count = %d, want 1", got)
	}
}

func TestHandleAccountRemoveRejectsNonLoopback(t *testing.T) {
	called := false
	server := NewServer(nil, "")
	server.SetAccountRemoveController(func(context.Context, string) (AccountReloadResult, error) {
		called = true
		return AccountReloadResult{}, nil
	})
	req := httptest.NewRequest(http.MethodPost, AccountRemovePath, bytes.NewBufferString(`{"bot_id":"bot@im.bot"}`))
	req.RemoteAddr = "192.0.2.1:12345"
	resp := httptest.NewRecorder()
	server.handleAccountRemove(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	if called {
		t.Fatal("controller should not be called for a non-loopback request")
	}
}

func TestHandleAccountRemoveRequiresBotID(t *testing.T) {
	server := NewServer(nil, "")
	server.SetAccountRemoveController(func(context.Context, string) (AccountReloadResult, error) {
		return AccountReloadResult{}, nil
	})
	req := httptest.NewRequest(http.MethodPost, AccountRemovePath, bytes.NewBufferString(`{}`))
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handleAccountRemove(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.Code)
	}
}

func TestHandleAccountRemoveSurfacesControllerError(t *testing.T) {
	server := NewServer(nil, "")
	server.SetAccountRemoveController(func(context.Context, string) (AccountReloadResult, error) {
		return AccountReloadResult{}, fmt.Errorf("account must be disabled before it can be removed")
	})
	req := httptest.NewRequest(http.MethodPost, AccountRemovePath, bytes.NewBufferString(`{"bot_id":"bot@im.bot"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handleAccountRemove(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "must be disabled") {
		t.Fatalf("body = %q, want it to surface the controller error", resp.Body.String())
	}
}

func TestHandleAccountRemoveUnavailableWithoutController(t *testing.T) {
	server := NewServer(nil, "")
	req := httptest.NewRequest(http.MethodPost, AccountRemovePath, bytes.NewBufferString(`{"bot_id":"bot@im.bot"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handleAccountRemove(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.Code)
	}
}

func TestHandleDeletedAccountsReturnsRecords(t *testing.T) {
	server := NewServer(nil, "")
	server.SetDeletedAccountsProvider(func() ([]ilink.DeletedAccount, error) {
		return []ilink.DeletedAccount{{BotID: "bot@im.bot", ILinkUserID: "scanner@im.wechat", RemovedAt: "2026-07-22T10:00:00Z"}}, nil
	})
	req := httptest.NewRequest(http.MethodGet, AccountsDeletedPath, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handleDeletedAccounts(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	var body struct {
		DeletedAccounts []ilink.DeletedAccount `json:"deleted_accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.DeletedAccounts) != 1 || body.DeletedAccounts[0].BotID != "bot@im.bot" {
		t.Fatalf("deleted accounts = %+v", body.DeletedAccounts)
	}
}

func TestHandleDeletedAccountsRejectsNonLoopback(t *testing.T) {
	server := NewServer(nil, "")
	server.SetDeletedAccountsProvider(func() ([]ilink.DeletedAccount, error) { return nil, nil })
	req := httptest.NewRequest(http.MethodGet, AccountsDeletedPath, nil)
	req.RemoteAddr = "192.0.2.1:12345"
	resp := httptest.NewRecorder()
	server.handleDeletedAccounts(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
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

func TestHandlePermissionsBlockIsIndependentOfLevelSave(t *testing.T) {
	stored := map[string]UserPermissionInfo{}
	server := NewServer(nil, "")
	server.SetPermissionController(func(_ context.Context, req PermissionSetRequest) (UserPermissionInfo, error) {
		// Mirrors cmd/start.go: a level/quota save must preserve whatever
		// Blocked was already set, never reset it.
		info := UserPermissionInfo{UserID: req.UserID, Level: req.Level, DailyLimit: req.DailyLimit, Blocked: stored[req.UserID].Blocked}
		stored[req.UserID] = info
		return info, nil
	})
	server.SetPermissionBlockController(func(_ context.Context, req PermissionBlockRequest) (UserPermissionInfo, error) {
		info := stored[req.UserID]
		info.UserID = req.UserID
		info.Blocked = req.Blocked
		stored[req.UserID] = info
		return info, nil
	})

	block := httptest.NewRequest(http.MethodPost, PermissionsBlockPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","blocked":true}`))
	block.RemoteAddr = "127.0.0.1:12345"
	blockResp := httptest.NewRecorder()
	server.handlePermissionsBlock(blockResp, block)
	if blockResp.Code != http.StatusOK || !strings.Contains(blockResp.Body.String(), `"blocked":true`) {
		t.Fatalf("block POST = %d %s", blockResp.Code, blockResp.Body.String())
	}

	// Saving a new level/quota afterwards must not clear the block.
	save := httptest.NewRequest(http.MethodPost, PermissionsPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","level":"workspace_write","daily_limit":30}`))
	save.RemoteAddr = "127.0.0.1:12345"
	saveResp := httptest.NewRecorder()
	server.handlePermissions(saveResp, save)
	if saveResp.Code != http.StatusOK {
		t.Fatalf("level save POST = %d %s", saveResp.Code, saveResp.Body.String())
	}
	if got := stored["someone@im.wechat"]; !got.Blocked {
		t.Fatalf("level save must not clear Blocked, got %+v", got)
	}

	// Unblocking must not touch the level/quota that was set above.
	unblock := httptest.NewRequest(http.MethodPost, PermissionsBlockPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","blocked":false}`))
	unblock.RemoteAddr = "127.0.0.1:12345"
	unblockResp := httptest.NewRecorder()
	server.handlePermissionsBlock(unblockResp, unblock)
	if unblockResp.Code != http.StatusOK {
		t.Fatalf("unblock POST = %d %s", unblockResp.Code, unblockResp.Body.String())
	}
	if got := stored["someone@im.wechat"]; got.Blocked || got.Level != "workspace_write" || got.DailyLimit != 30 {
		t.Fatalf("unblock must preserve level/quota, got %+v", got)
	}
}

func TestHandlePermissionsBlockRejectsNonLoopback(t *testing.T) {
	called := false
	server := NewServer(nil, "")
	server.SetPermissionBlockController(func(_ context.Context, req PermissionBlockRequest) (UserPermissionInfo, error) {
		called = true
		return UserPermissionInfo{}, nil
	})
	req := httptest.NewRequest(http.MethodPost, PermissionsBlockPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","blocked":true}`))
	req.RemoteAddr = "192.0.2.1:12345"
	resp := httptest.NewRecorder()
	server.handlePermissionsBlock(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	if called {
		t.Fatal("controller should not be called for a non-loopback request")
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

func TestHandlePersonasListsAndSaves(t *testing.T) {
	stored := map[string]PersonaInfo{"default": {Name: "default", Text: "通用助手"}}
	server := NewServer(nil, "")
	server.SetPersonasProvider(func() ([]PersonaInfo, error) {
		out := make([]PersonaInfo, 0, len(stored))
		for _, v := range stored {
			out = append(out, v)
		}
		return out, nil
	})
	server.SetPersonaSaveController(func(_ context.Context, req PersonaSaveRequest) (PersonaInfo, error) {
		info := PersonaInfo{Name: req.Name, Text: req.Text}
		stored[req.Name] = info
		return info, nil
	})

	get := httptest.NewRequest(http.MethodGet, PersonasPath, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResp := httptest.NewRecorder()
	server.handlePersonas(getResp, get)
	if getResp.Code != http.StatusOK || !strings.Contains(getResp.Body.String(), "通用助手") {
		t.Fatalf("GET = %d %s", getResp.Code, getResp.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, PersonasPath, bytes.NewBufferString(`{"name":"vip","text":"VIP 人格"}`))
	post.RemoteAddr = "127.0.0.1:12345"
	postResp := httptest.NewRecorder()
	server.handlePersonas(postResp, post)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", postResp.Code, postResp.Body.String())
	}
	if got := stored["vip"]; got.Text != "VIP 人格" {
		t.Fatalf("stored persona = %+v", got)
	}
}

func TestHandlePersonaDeleteRejectsDefault(t *testing.T) {
	server := NewServer(nil, "")
	server.SetPersonaDeleteController(func(_ context.Context, req PersonaDeleteRequest) error {
		if req.Name == "default" {
			return fmt.Errorf("cannot delete the %q persona", "default")
		}
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, PersonaDeletePath, bytes.NewBufferString(`{"name":"default"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	resp := httptest.NewRecorder()
	server.handlePersonaDelete(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.Code)
	}
}

func TestHandlePermissionsPersonaIsIndependentOfLevelSave(t *testing.T) {
	stored := map[string]UserPermissionInfo{}
	server := NewServer(nil, "")
	server.SetPermissionController(func(_ context.Context, req PermissionSetRequest) (UserPermissionInfo, error) {
		info := UserPermissionInfo{UserID: req.UserID, Level: req.Level, DailyLimit: req.DailyLimit, Persona: stored[req.UserID].Persona}
		stored[req.UserID] = info
		return info, nil
	})
	server.SetPermissionPersonaController(func(_ context.Context, req PermissionPersonaRequest) (UserPermissionInfo, error) {
		info := stored[req.UserID]
		info.UserID = req.UserID
		info.Persona = req.Persona
		stored[req.UserID] = info
		return info, nil
	})

	bind := httptest.NewRequest(http.MethodPost, PermissionsPersonaPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","persona":"vip"}`))
	bind.RemoteAddr = "127.0.0.1:12345"
	bindResp := httptest.NewRecorder()
	server.handlePermissionsPersona(bindResp, bind)
	if bindResp.Code != http.StatusOK || !strings.Contains(bindResp.Body.String(), "vip") {
		t.Fatalf("bind POST = %d %s", bindResp.Code, bindResp.Body.String())
	}

	save := httptest.NewRequest(http.MethodPost, PermissionsPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","level":"workspace_write","daily_limit":30}`))
	save.RemoteAddr = "127.0.0.1:12345"
	saveResp := httptest.NewRecorder()
	server.handlePermissions(saveResp, save)
	if saveResp.Code != http.StatusOK {
		t.Fatalf("level save POST = %d %s", saveResp.Code, saveResp.Body.String())
	}
	if got := stored["someone@im.wechat"]; got.Persona != "vip" {
		t.Fatalf("level save must not clear persona binding, got %+v", got)
	}

	// Saving new level/quota afterwards must not clear the persona binding.
	save2 := httptest.NewRequest(http.MethodPost, PermissionsPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","level":"full_access","daily_limit":50}`))
	save2.RemoteAddr = "127.0.0.1:12345"
	save2Resp := httptest.NewRecorder()
	server.handlePermissions(save2Resp, save2)
	if save2Resp.Code != http.StatusOK {
		t.Fatalf("second level save POST = %d %s", save2Resp.Code, save2Resp.Body.String())
	}
	if got := stored["someone@im.wechat"]; got.Persona != "vip" {
		t.Fatalf("second level save must not clear persona binding, got %+v", got)
	}

	// Binding a new persona must not touch the level/quota that was set above.
	bind2 := httptest.NewRequest(http.MethodPost, PermissionsPersonaPath, bytes.NewBufferString(`{"user_id":"someone@im.wechat","persona":"premium"}`))
	bind2.RemoteAddr = "127.0.0.1:12345"
	bind2Resp := httptest.NewRecorder()
	server.handlePermissionsPersona(bind2Resp, bind2)
	if bind2Resp.Code != http.StatusOK {
		t.Fatalf("second bind POST = %d %s", bind2Resp.Code, bind2Resp.Body.String())
	}
	if got := stored["someone@im.wechat"]; got.Persona != "premium" || got.Level != "full_access" || got.DailyLimit != 50 {
		t.Fatalf("second bind must preserve level/quota, got %+v", got)
	}
}

func TestHandleNonOwnerRoutingReadsAndUpdates(t *testing.T) {
	stored := NonOwnerRoutingSettings{DefaultAgent: "codex-shared", AllowSwitch: false}
	server := NewServer(nil, "")
	server.SetNonOwnerRoutingProvider(func() NonOwnerRoutingSettings { return stored })
	server.SetNonOwnerRoutingController(func(_ context.Context, s NonOwnerRoutingSettings) (NonOwnerRoutingSettings, error) {
		stored = s
		return stored, nil
	})

	get := httptest.NewRequest(http.MethodGet, NonOwnerRoutingPath, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResp := httptest.NewRecorder()
	server.handleNonOwnerRouting(getResp, get)
	if getResp.Code != http.StatusOK || !strings.Contains(getResp.Body.String(), "codex-shared") {
		t.Fatalf("GET = %d %s", getResp.Code, getResp.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, NonOwnerRoutingPath, bytes.NewBufferString(`{"default_agent":"claude","allow_switch":true}`))
	post.RemoteAddr = "127.0.0.1:12345"
	postResp := httptest.NewRecorder()
	server.handleNonOwnerRouting(postResp, post)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", postResp.Code, postResp.Body.String())
	}
	if stored.DefaultAgent != "claude" || !stored.AllowSwitch {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestHandleNonOwnerRoutingRejectsNonLoopback(t *testing.T) {
	called := false
	server := NewServer(nil, "")
	server.SetNonOwnerRoutingController(func(_ context.Context, s NonOwnerRoutingSettings) (NonOwnerRoutingSettings, error) {
		called = true
		return s, nil
	})
	req := httptest.NewRequest(http.MethodPost, NonOwnerRoutingPath, bytes.NewBufferString(`{}`))
	req.RemoteAddr = "192.0.2.1:12345"
	resp := httptest.NewRecorder()
	server.handleNonOwnerRouting(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	if called {
		t.Fatal("controller should not be called for a non-loopback request")
	}
}

func TestHandleAgentModelReadsAndUpdates(t *testing.T) {
	stored := AgentModelSettingsResponse{Agents: map[string]AgentModelSettings{
		"codex-shared": {},
		"claude":       {},
	}}
	server := NewServer(nil, "")
	server.SetAgentModelProvider(func() AgentModelSettingsResponse { return stored })
	server.SetAgentModelController(func(_ context.Context, req AgentModelUpdateRequest) (AgentModelSettingsResponse, error) {
		stored.Agents[req.Agent] = AgentModelSettings{Model: req.Model, ModelReasoningEffort: req.ModelReasoningEffort}
		return stored, nil
	})

	get := httptest.NewRequest(http.MethodGet, AgentModelPath, nil)
	get.RemoteAddr = "127.0.0.1:12345"
	getResp := httptest.NewRecorder()
	server.handleAgentModel(getResp, get)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", getResp.Code, getResp.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, AgentModelPath, bytes.NewBufferString(`{"agent":"codex-shared","model":"gpt-5.5","model_reasoning_effort":"low"}`))
	post.RemoteAddr = "127.0.0.1:12345"
	postResp := httptest.NewRecorder()
	server.handleAgentModel(postResp, post)
	if postResp.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", postResp.Code, postResp.Body.String())
	}
	got := stored.Agents["codex-shared"]
	if got.Model != "gpt-5.5" || got.ModelReasoningEffort != "low" {
		t.Fatalf("stored codex-shared settings = %+v", got)
	}
	if stored.Agents["claude"] != (AgentModelSettings{}) {
		t.Fatalf("updating codex-shared must not touch claude's settings, got %+v", stored.Agents["claude"])
	}
}

func TestHandleAgentModelRejectsNonLoopback(t *testing.T) {
	called := false
	server := NewServer(nil, "")
	server.SetAgentModelController(func(_ context.Context, req AgentModelUpdateRequest) (AgentModelSettingsResponse, error) {
		called = true
		return AgentModelSettingsResponse{}, nil
	})
	req := httptest.NewRequest(http.MethodPost, AgentModelPath, bytes.NewBufferString(`{}`))
	req.RemoteAddr = "192.0.2.1:12345"
	resp := httptest.NewRecorder()
	server.handleAgentModel(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	if called {
		t.Fatal("controller should not be called for a non-loopback request")
	}
}
