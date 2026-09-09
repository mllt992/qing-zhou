// Package telegram is a tiny Bot API client. The panel talks to Telegram over
// plain HTTPS JSON — there is no third-party SDK, so a token change is just
// another settings write and tests can point the client at httptest.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAPI = "https://api.telegram.org"

// Client calls one bot token. Construct a new one when the token changes;
// nothing here is cached across tokens.
type Client struct {
	Token   string
	HTTP    *http.Client
	APIBase string // empty = https://api.telegram.org; tests override
}

func (c *Client) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 40 * time.Second}
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return defaultAPI
}

// User is the subset of Telegram's User we actually read.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // private | group | supergroup | channel
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// BotCommand is one entry in Telegram's slash-command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Result      json.RawMessage `json:"result"`
}

func (c *Client) call(ctx context.Context, method string, payload any, out any) error {
	if c == nil || c.Token == "" {
		return fmt.Errorf("telegram: bot token not configured")
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	url := c.apiBase() + "/bot" + c.Token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	const maxResponseBytes = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("telegram: response exceeds 1 MiB")
	}
	var wrap apiResp
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return fmt.Errorf("telegram: bad response (%d): %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if resp.StatusCode != http.StatusOK && wrap.OK {
		return &APIError{Code: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
	}
	if !wrap.OK {
		return &APIError{Code: wrap.ErrorCode, Description: wrap.Description}
	}
	if out == nil || len(wrap.Result) == 0 {
		return nil
	}
	return json.Unmarshal(wrap.Result, out)
}

// APIError is a Bot API-level failure (unauthorized token, chat not found, …).
type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("telegram: api error %d", e.Code)
	}
	return "telegram: " + e.Description
}

func (e *APIError) Unauthorized() bool { return e.Code == 401 }

func GetMe(ctx context.Context, c *Client) (*User, error) {
	var u User
	if err := c.call(ctx, "getMe", map[string]any{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteWebhook clears any leftover webhook so getUpdates long-polling works.
// A previous install (or a bot used with another host) may have one set; the
// two modes cannot run together.
func DeleteWebhook(ctx context.Context, c *Client) error {
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func GetUpdates(ctx context.Context, c *Client, offset int64, timeoutSec int) ([]Update, error) {
	var out []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message"},
	}, &out)
	if out == nil {
		out = []Update{}
	}
	return out, err
}

// SetCommands replaces the bot's default command menu. Passing an empty slice
// is valid and clears the menu.
func SetCommands(ctx context.Context, c *Client, commands []BotCommand) error {
	if commands == nil {
		commands = []BotCommand{}
	}
	return c.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
}

// SendHTML delivers a private-chat message. disable_web_page_preview keeps a
// subscription URL from turning into a Telegram-side preview card that would
// fetch the link (and log it) on Telegram's servers a second time.
func SendHTML(ctx context.Context, c *Client, chatID int64, html string) error {
	return c.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}, nil)
}

// Escape makes s safe to interpolate into a parse_mode=HTML message.
func Escape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
