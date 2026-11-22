package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BridgeConfig describes the runtime connection file written by iterm_bridge.py.
type BridgeConfig struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

// BridgeClient talks to the local iterm_bridge.py HTTP server.
type BridgeClient struct {
	configPath string
	staticURL  string
	staticTok  string
	httpClient *http.Client
}

// DefaultBridgeConfigPath returns ~/.weclaw/iterm-bridge.json.
func DefaultBridgeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "iterm-bridge.json"), nil
}

// NewBridgeClient creates a client that reloads addr/token from configPath on
// each call so restarting iterm_bridge.py works without restarting weclaw.
func NewBridgeClient(configPath string) *BridgeClient {
	return &BridgeClient{
		configPath: configPath,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// NewStaticBridgeClient creates a client pinned to a fixed URL and token (for tests).
func NewStaticBridgeClient(baseURL, token string) *BridgeClient {
	return NewStaticBridgeClientWithHTTP(baseURL, token, nil)
}

// NewStaticBridgeClientWithHTTP creates a client with a custom http.Client (for tests).
func NewStaticBridgeClientWithHTTP(baseURL, token string, hc *http.Client) *BridgeClient {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &BridgeClient{
		staticURL:  strings.TrimRight(baseURL, "/"),
		staticTok:  token,
		httpClient: hc,
	}
}

func (c *BridgeClient) resolveConfig() (BridgeConfig, error) {
	if c.staticURL != "" {
		return BridgeConfig{Addr: c.staticURL, Token: c.staticTok}, nil
	}
	path := c.configPath
	if path == "" {
		var err error
		path, err = DefaultBridgeConfigPath()
		if err != nil {
			return BridgeConfig{}, fmt.Errorf("resolve bridge config path: %w", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return BridgeConfig{}, fmt.Errorf("iTerm 助手未启动（未找到 %s）", path)
		}
		return BridgeConfig{}, fmt.Errorf("read bridge config: %w", err)
	}
	var cfg BridgeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return BridgeConfig{}, fmt.Errorf("parse bridge config: %w", err)
	}
	cfg.Addr = strings.TrimRight(strings.TrimSpace(cfg.Addr), "/")
	if cfg.Addr == "" || cfg.Token == "" {
		return BridgeConfig{}, fmt.Errorf("iTerm 助手配置不完整（addr/token 为空）")
	}
	return cfg, nil
}

func (c *BridgeClient) doJSON(ctx context.Context, method, path string, reqBody any, respBody any) error {
	cfg, err := c.resolveConfig()
	if err != nil {
		return err
	}
	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.Addr+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Bridge-Token", cfg.Token)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("连接 iTerm 助手失败（请确认 iTerm2 与助手在运行）: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("iTerm 助手返回错误 (%d): %s", resp.StatusCode, msg)
	}
	if respBody != nil && len(data) > 0 {
		if err := json.Unmarshal(data, respBody); err != nil {
			return fmt.Errorf("解析 iTerm 助手响应失败: %w", err)
		}
	}
	return nil
}

func (c *BridgeClient) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/sessions", nil, &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *BridgeClient) NewSession(ctx context.Context, cwd string) (SessionInfo, error) {
	var out struct {
		Session SessionInfo `json:"session"`
	}
	req := map[string]string{}
	if strings.TrimSpace(cwd) != "" {
		req["cwd"] = strings.TrimSpace(cwd)
	}
	if err := c.doJSON(ctx, http.MethodPost, "/sessions/new", req, &out); err != nil {
		return SessionInfo{}, err
	}
	return out.Session, nil
}

func (c *BridgeClient) SendText(ctx context.Context, sessionID, text string, submit bool) (SendResult, error) {
	var out SendResult
	req := map[string]any{
		"session_id": sessionID,
		"text":       text,
		"submit":     submit,
	}
	if err := c.doJSON(ctx, http.MethodPost, "/sessions/send", req, &out); err != nil {
		return SendResult{}, err
	}
	return out, nil
}

func (c *BridgeClient) SendKey(ctx context.Context, sessionID, key string) error {
	req := map[string]string{
		"session_id": sessionID,
		"key":        key,
	}
	return c.doJSON(ctx, http.MethodPost, "/sessions/key", req, nil)
}

func (c *BridgeClient) Snap(ctx context.Context, sessionID string) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	path := "/sessions/snap?session_id=" + url.QueryEscape(sessionID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}

func (c *BridgeClient) CloseSession(ctx context.Context, sessionID string) error {
	req := map[string]string{"session_id": sessionID}
	return c.doJSON(ctx, http.MethodPost, "/sessions/close", req, nil)
}
