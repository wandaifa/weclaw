package cmd

import (
	"context"
	"fmt"
	"sync"

	"github.com/fastclaw-ai/weclaw/api"
	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
)

type monitorRunner func(context.Context, *ilink.Credentials, *messaging.Handler, func(string))

type managedAccount struct {
	credentials ilink.Credentials
	client      *ilink.Client
	cancel      context.CancelFunc
	status      string
}

type accountReloadResult struct {
	Clients  []*ilink.Client
	Added    int
	Replaced int
}

type accountManager struct {
	ctx      context.Context
	handler  *messaging.Handler
	run      monitorRunner
	mu       sync.Mutex
	accounts map[string]*managedAccount
	order    []string
	wg       sync.WaitGroup
}

func newAccountManager(ctx context.Context, handler *messaging.Handler) *accountManager {
	return newAccountManagerWithRunner(ctx, handler, runMonitorWithRestart)
}

func newAccountManagerWithRunner(ctx context.Context, handler *messaging.Handler, run monitorRunner) *accountManager {
	return &accountManager{
		ctx:      ctx,
		handler:  handler,
		run:      run,
		accounts: make(map[string]*managedAccount),
	}
}

func (m *accountManager) Reload(credentials []*ilink.Credentials) (accountReloadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ctx.Err(); err != nil {
		return accountReloadResult{}, err
	}

	result := accountReloadResult{}
	desired := make(map[string]bool, len(credentials))
	for _, raw := range credentials {
		if raw == nil || raw.ILinkBotID == "" {
			return accountReloadResult{}, fmt.Errorf("invalid account credentials")
		}
		desired[raw.ILinkBotID] = true

		creds := *raw
		existing, exists := m.accounts[creds.ILinkBotID]
		if exists && credentialsEqual(existing.credentials, creds) {
			continue
		}

		accountCtx, cancel := context.WithCancel(m.ctx)
		account := &managedAccount{
			credentials: creds,
			client:      ilink.NewClient(&creds),
			cancel:      cancel,
			status:      "starting",
		}
		if exists {
			existing.cancel()
			result.Replaced++
		} else {
			m.order = append(m.order, creds.ILinkBotID)
			result.Added++
		}
		m.accounts[creds.ILinkBotID] = account
		m.startMonitor(accountCtx, creds)
	}
	for botID, account := range m.accounts {
		if desired[botID] {
			continue
		}
		account.cancel()
		delete(m.accounts, botID)
	}
	m.order = m.order[:0]
	for _, raw := range credentials {
		if raw != nil && m.accounts[raw.ILinkBotID] != nil {
			m.order = append(m.order, raw.ILinkBotID)
		}
	}

	result.Clients = m.clientsLocked()
	return result, nil
}

func (m *accountManager) startMonitor(ctx context.Context, credentials ilink.Credentials) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(ctx, &credentials, m.handler, func(status string) {
			m.setStatus(credentials.ILinkBotID, credentials, status)
		})
	}()
}

func (m *accountManager) setStatus(botID string, credentials ilink.Credentials, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	account := m.accounts[botID]
	if account != nil && credentialsEqual(account.credentials, credentials) {
		account.status = status
	}
}

func (m *accountManager) Statuses() []api.AccountStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	statuses := make([]api.AccountStatus, 0, len(m.order))
	for _, botID := range m.order {
		if account := m.accounts[botID]; account != nil {
			statuses = append(statuses, api.AccountStatus{BotID: botID, ILinkUserID: account.credentials.ILinkUserID, Status: account.status})
		}
	}
	return statuses
}

func (m *accountManager) clientsLocked() []*ilink.Client {
	clients := make([]*ilink.Client, 0, len(m.order))
	for _, botID := range m.order {
		if account := m.accounts[botID]; account != nil {
			clients = append(clients, account.client)
		}
	}
	return clients
}

func (m *accountManager) Wait() {
	m.wg.Wait()
}

func credentialsEqual(a, b ilink.Credentials) bool {
	return a.BotToken == b.BotToken &&
		a.ILinkBotID == b.ILinkBotID &&
		a.BaseURL == b.BaseURL &&
		a.ILinkUserID == b.ILinkUserID
}
