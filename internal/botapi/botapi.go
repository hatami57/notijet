// Package botapi is the sendMessage client shared by the providers for
// Telegram-compatible bot APIs (Telegram, Bale): https://<host>/bot<token>/<method>
// with {"ok", "result"} / {"ok": false, "error_code", "description"} replies.
package botapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/flexjson"
	"github.com/hatami57/notijet/internal/httpc"
)

// MaxText is the longest message text, in characters, sendMessage accepts.
const MaxText = 4096

// Config describes one bot API and one bot.
type Config struct {
	Provider string
	BaseURL  string
	Token    string
	// ParseMode reports whether the API takes parse_mode, which lets a
	// message be sent as HTML. Without it, HTML bodies are unsupported.
	ParseMode bool
	// DefaultChat receives messages that have no recipient.
	DefaultChat string
	// Silent sends messages without a notification sound.
	Silent bool
	// NoLinkPreview disables link previews.
	NoLinkPreview bool
	HTTPClient    *http.Client
}

// Client sends messages through a bot API.
type Client struct {
	cfg    Config
	client *httpc.Client
}

// New returns a Client for cfg.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	cfg.Token = strings.TrimSpace(cfg.Token)
	return &Client{cfg: cfg, client: httpc.New(cfg.Provider, cfg.HTTPClient)}
}

type request struct {
	ChatID              string       `json:"chat_id"`
	Text                string       `json:"text"`
	ParseMode           string       `json:"parse_mode,omitempty"`
	DisableNotification bool         `json:"disable_notification,omitempty"`
	LinkPreviewOptions  *linkPreview `json:"link_preview_options,omitempty"`
}

type linkPreview struct {
	IsDisabled bool `json:"is_disabled"`
}

type reply struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Result      struct {
		MessageID flexjson.String `json:"message_id"`
	} `json:"result"`
}

// Send delivers msg to each recipient (chat IDs or @usernames), or to
// DefaultChat when it has none. Message.Subject, when set, is sent as a first
// line above the text. Message.Data["parse_mode"] sets the parse mode of a
// Text message where the API supports one; an HTML body is sent with
// parse_mode HTML.
func (c *Client) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	name := c.cfg.Provider
	if err := notijet.CheckChat(name, msg, c.cfg.DefaultChat == ""); err != nil {
		return nil, err
	}
	if c.cfg.Token == "" {
		return nil, notijet.Invalid(name, "bot token is empty")
	}
	text, parseMode := msg.Text, msg.Data["parse_mode"]
	if strings.TrimSpace(msg.HTML) != "" {
		if !c.cfg.ParseMode {
			return nil, notijet.Unsupported(name, "HTML body (set Text)")
		}
		text, parseMode = msg.HTML, "HTML"
	} else if parseMode != "" && !c.cfg.ParseMode {
		return nil, notijet.Unsupported(name, "parse_mode")
	}
	if s := strings.TrimSpace(msg.Subject); s != "" {
		text = s + "\n\n" + text
	}
	if n := utf8.RuneCountInString(text); n > MaxText {
		return nil, notijet.Invalid(name, "message text is "+strconv.Itoa(n)+" characters; the limit is 4096")
	}

	to := msg.Recipients()
	if len(to) == 0 {
		to = []string{c.cfg.DefaultChat}
	}
	endpoint := c.cfg.BaseURL + "/bot" + c.cfg.Token + "/sendMessage"
	return notijet.SendBatches(ctx, name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		req := request{ChatID: batch[0], Text: text, ParseMode: parseMode, DisableNotification: c.cfg.Silent}
		if c.cfg.NoLinkPreview {
			req.LinkPreviewOptions = &linkPreview{IsDisabled: true}
		}
		r, err := httpc.JSON(http.MethodPost, endpoint, req)
		if err != nil {
			return nil, notijet.Fault(name, "encoding request failed", err)
		}
		resp, err := c.client.Do(ctx, r)
		if err != nil {
			return nil, err
		}
		var out reply
		if err := c.client.Decode(resp, &out); err != nil {
			if !resp.OK() {
				return nil, mapError(name, resp.Status, resp.Snippet())
			}
			return nil, err
		}
		if !out.OK {
			code := out.ErrorCode
			if code == 0 {
				code = resp.Status
			}
			return nil, mapError(name, code, out.Description)
		}
		if out.Result.MessageID == "" {
			return nil, c.client.Unexpected(resp, "")
		}
		return []notijet.Receipt{{To: batch[0], MessageID: string(out.Result.MessageID), Status: "sent"}}, nil
	})
}

// mapError turns a bot API error_code and description into a notijet error.
func mapError(provider string, code int, description string) error {
	c := strconv.Itoa(code)
	lower := strings.ToLower(description)
	switch {
	case code == http.StatusUnauthorized || code == http.StatusNotFound:
		// An invalid token yields 401, or 404 for an unknown bot path.
		return notijet.Rejected(provider, c, description, notijet.ErrUnauthorized)
	case code == http.StatusForbidden:
		// Blocked by the user, kicked from the group, or not allowed to post.
		return notijet.Rejected(provider, c, description, notijet.ErrInvalidRecipient)
	case code == http.StatusTooManyRequests:
		return notijet.Throttled(provider, c, description)
	case strings.Contains(lower, "chat not found") || strings.Contains(lower, "user not found") ||
		strings.Contains(lower, "peer_id_invalid") || strings.Contains(lower, "chat_id is empty"):
		return notijet.Rejected(provider, c, description, notijet.ErrInvalidRecipient)
	case code >= 500:
		return notijet.FaultCode(provider, c, description, nil)
	}
	return notijet.Rejected(provider, c, description, nil)
}
