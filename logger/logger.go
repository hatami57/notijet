// Package logger is a notijet provider that writes messages to a slog.Logger
// instead of delivering them, for development: wire it in place of a real
// provider and read OTP codes and email bodies from the console.
//
// It logs message content and recipients, so never use it where logs are
// shipped somewhere sensitive data must not reach.
package logger

import (
	"context"
	"crypto/rand"
	"log/slog"

	"github.com/hatami57/notijet"
)

// Provider logs each message it is asked to send.
type Provider struct {
	log     *slog.Logger
	name    string
	channel notijet.Channel
	level   slog.Level
}

// Option configures a Provider.
type Option func(*Provider)

// WithName sets the provider name (default "logger").
func WithName(name string) Option {
	return func(p *Provider) { p.name = name }
}

// WithChannel sets the channel the provider reports and validates messages for
// (default notijet.ChannelSMS).
func WithChannel(ch notijet.Channel) Option {
	return func(p *Provider) { p.channel = ch }
}

// WithLevel sets the level messages are logged at (default slog.LevelInfo).
func WithLevel(level slog.Level) Option {
	return func(p *Provider) { p.level = level }
}

// New returns a provider that logs to l; nil means slog.Default().
func New(l *slog.Logger, opts ...Option) *Provider {
	if l == nil {
		l = slog.Default()
	}
	p := &Provider{log: l, name: "logger", channel: notijet.ChannelSMS, level: slog.LevelInfo}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Provider) Name() string             { return p.name }
func (p *Provider) Channel() notijet.Channel { return p.channel }

// Send validates msg like a real provider of its channel, logs it and returns a
// single receipt with a random message ID.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, notijet.Fault(p.name, "context done", err)
	}
	if err := notijet.Check(p.name, p.channel, msg); err != nil {
		return nil, err
	}
	id := rand.Text()
	p.log.Log(ctx, p.level, "notijet: message", attrs(p, id, msg)...)
	return &notijet.Result{
		Provider: p.name,
		Receipts: []notijet.Receipt{{MessageID: id, Status: "logged"}},
	}, nil
}

func attrs(p *Provider, id string, msg *notijet.Message) []any {
	a := []any{
		slog.String("provider", p.name),
		slog.String("channel", string(p.channel)),
		slog.String("message_id", id),
		slog.Any("to", msg.Recipients()),
	}
	str := func(key, v string) {
		if v != "" {
			a = append(a, slog.String(key, v))
		}
	}
	list := func(key string, v []string) {
		if v = notijet.CleanList(v); len(v) > 0 {
			a = append(a, slog.Any(key, v))
		}
	}
	list("cc", msg.CC)
	list("bcc", msg.BCC)
	list("reply_to", msg.ReplyTo)
	str("id", msg.ID)
	str("from", msg.From)
	str("subject", msg.Subject)
	str("text", msg.Text)
	str("html", msg.HTML)
	if t := msg.Template; t != nil {
		a = append(a, slog.String("template", t.ID), slog.Any("params", t.Map()))
	}
	if n := len(msg.Attachments); n > 0 {
		names := make([]string, n)
		for i, att := range msg.Attachments {
			names[i] = att.Filename
			if att.Inline() && att.Filename == "" {
				names[i] = "cid:" + att.ContentID
			}
		}
		a = append(a, slog.Any("attachments", names))
	}
	if len(msg.Data) > 0 {
		a = append(a, slog.Any("data", msg.Data))
	}
	if len(msg.Metadata) > 0 {
		a = append(a, slog.Any("metadata", msg.Metadata))
	}
	return a
}
