// Package telegram is a notijet chat provider for the Telegram Bot API
// (https://core.telegram.org/bots/api#sendmessage).
//
//	p := telegram.New(botToken, telegram.WithChatID("-1001234567890"))
//	p.Send(ctx, &notijet.Message{Text: "Deploy finished"})
//
// Recipients are chat IDs or @channel usernames; a message without one goes
// to the WithChatID chat. Each recipient gets its own sendMessage call. An
// HTML body is sent with parse_mode HTML (Telegram's subset of tags); for a
// Text body, Message.Data["parse_mode"] may set "MarkdownV2", "Markdown" or
// "HTML". Message.Subject, when set, becomes the first line.
package telegram

import (
	"context"
	"net/http"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/botapi"
)

// Name is the provider name.
const Name = "telegram"

// DefaultBaseURL is the Telegram Bot API root.
const DefaultBaseURL = "https://api.telegram.org"

// Provider sends messages through a Telegram bot.
type Provider struct {
	client *botapi.Client
}

// Option configures a Provider.
type Option func(*botapi.Config)

// WithChatID sets the chat that receives messages without recipients.
func WithChatID(chatID string) Option { return func(c *botapi.Config) { c.DefaultChat = chatID } }

// WithSilent sends messages without a notification sound.
func WithSilent() Option { return func(c *botapi.Config) { c.Silent = true } }

// WithoutLinkPreview disables link previews.
func WithoutLinkPreview() Option { return func(c *botapi.Config) { c.NoLinkPreview = true } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(c *botapi.Config) { c.HTTPClient = hc } }

// WithBaseURL overrides the API root, for a local Bot API server or tests.
func WithBaseURL(u string) Option { return func(c *botapi.Config) { c.BaseURL = u } }

// New returns a Telegram provider for the bot token from @BotFather.
func New(botToken string, opts ...Option) *Provider {
	cfg := botapi.Config{Provider: Name, BaseURL: DefaultBaseURL, Token: botToken, ParseMode: true}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Provider{client: botapi.New(cfg)}
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelChat }

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	return p.client.Send(ctx, msg)
}
