package cmd

import (
	"context"
	"fmt"
	"sync"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
)

type monitorRunner func(context.Context, *ilink.Credentials, *messaging.Handler)

type managedAccount struct {
	credentials ilink.Credentials
	client      *ilink.Client
	cancel      context.CancelFunc
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
	for _, raw := range credentials {
		if raw == nil || raw.ILinkBotID == "" {
			return accountReloadResult{}, fmt.Errorf("invalid account credentials")
		}

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

	result.Clients = m.clientsLocked()
	return result, nil
}

func (m *accountManager) startMonitor(ctx context.Context, credentials ilink.Credentials) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(ctx, &credentials, m.handler)
	}()
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
