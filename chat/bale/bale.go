// Package bale is a notijet chat provider for Bale messenger bots
// (https://docs.bale.ai), whose bot API is compatible with Telegram's.
//
//	p := bale.New(botToken, bale.WithChatID("1234567890"))
//	p.Send(ctx, &notijet.Message{Text: "سفارش شما ارسال شد"})
//
// Recipients are chat IDs; a message without one goes to the WithChatID
// chat. Each recipient gets its own sendMessage call. Bale renders every
// message as Markdown and has no parse_mode, so HTML bodies and
// Data["parse_mode"] are unsupported. Message.Subject, when set, becomes the
// first line.
package bale

import (
	"context"
	"net/http"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/botapi"
)

// Name is the provider name.
const Name = "bale"

// DefaultBaseURL is the Bale bot API root.
const DefaultBaseURL = "https://tapi.bale.ai"

// Provider sends messages through a Bale bot.
type Provider struct {
	client *botapi.Client
}

// Option configures a Provider.
type Option func(*botapi.Config)

// WithChatID sets the chat that receives messages without recipients.
func WithChatID(chatID string) Option { return func(c *botapi.Config) { c.DefaultChat = chatID } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(c *botapi.Config) { c.HTTPClient = hc } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option { return func(c *botapi.Config) { c.BaseURL = u } }

// New returns a Bale provider for the bot token from @botfather.
func New(botToken string, opts ...Option) *Provider {
	cfg := botapi.Config{Provider: Name, BaseURL: DefaultBaseURL, Token: botToken}
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
