package notijet

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hatami57/microjet/core/errorx"
)

// Notifier holds named providers and a default provider per channel, so call
// sites send by channel ("sms", "email") or by name ("kavenegar-otp") without
// knowing which platform is behind it. It runs hooks around every send and logs
// each one through slog. A Notifier is safe for concurrent use.
//
//	n := notijet.NewNotifier(notijet.WithLogger(logger))
//	n.Register("otp", notijet.Failover(kavenegarOTP, smsirOTP))
//	n.Register("mail", smtpProvider)
//	_, err := n.Send(ctx, notijet.ChannelSMS, msg)
type Notifier struct {
	mu        sync.RWMutex
	providers map[string]Provider
	names     []string
	defaults  map[Channel]string
	hooks     []Hooks
	log       *slog.Logger
}

// NotifierOption configures a Notifier.
type NotifierOption func(*Notifier)

// WithLogger sets the logger the Notifier records sends with: successful sends
// at Debug, failures at Warn. Recipients and content are never logged, only
// their count. A nil logger disables logging. The default is slog.Default().
func WithLogger(l *slog.Logger) NotifierOption {
	return func(n *Notifier) {
		if l == nil {
			l = slog.New(slog.DiscardHandler)
		}
		n.log = l
	}
}

// NewNotifier returns an empty Notifier.
func NewNotifier(opts ...NotifierOption) *Notifier {
	n := &Notifier{
		providers: map[string]Provider{},
		defaults:  map[Channel]string{},
		log:       slog.Default(),
	}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// Register adds p under name; an empty name means p.Name(). The first provider
// registered for a channel becomes that channel's default (see SetDefault).
// Registering a nil provider or a name already in use is an error.
func (n *Notifier) Register(name string, p Provider) error {
	if p == nil {
		return Invalid("notifier", "provider is nil")
	}
	if name = strings.TrimSpace(name); name == "" {
		name = p.Name()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.providers[name]; ok {
		return Invalid("notifier", "provider "+name+" is already registered")
	}
	n.providers[name] = p
	n.names = append(n.names, name)
	if _, ok := n.defaults[p.Channel()]; !ok {
		n.defaults[p.Channel()] = name
	}
	return nil
}

// MustRegister is Register that panics on error, for wiring at startup.
func (n *Notifier) MustRegister(name string, p Provider) *Notifier {
	if err := n.Register(name, p); err != nil {
		panic(err)
	}
	return n
}

// SetDefault makes the provider registered under name the default for its
// channel.
func (n *Notifier) SetDefault(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	p, ok := n.providers[name]
	if !ok {
		return noProvider("no provider named " + name)
	}
	n.defaults[p.Channel()] = name
	return nil
}

// AddHooks adds hooks that run around every send made through the Notifier, in
// the order they were added.
func (n *Notifier) AddHooks(h Hooks) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hooks = append(n.hooks, h)
}

// Provider returns the provider registered under name.
func (n *Notifier) Provider(name string) (Provider, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	p, ok := n.providers[name]
	if !ok {
		return nil, noProvider("no provider named " + name)
	}
	return p, nil
}

// Default returns the name and provider used for ch.
func (n *Notifier) Default(ch Channel) (string, Provider, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	name, ok := n.defaults[ch]
	if !ok {
		return "", nil, noProvider("no provider for channel " + string(ch))
	}
	return name, n.providers[name], nil
}

// Names returns the registered provider names in registration order.
func (n *Notifier) Names() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return slices.Clone(n.names)
}

// Send delivers msg through the default provider for ch.
func (n *Notifier) Send(ctx context.Context, ch Channel, msg *Message) (*Result, error) {
	name, p, err := n.Default(ch)
	if err != nil {
		return nil, err
	}
	return n.send(ctx, name, p, msg)
}

// SendVia delivers msg through the provider registered under name.
func (n *Notifier) SendVia(ctx context.Context, name string, msg *Message) (*Result, error) {
	p, err := n.Provider(name)
	if err != nil {
		return nil, err
	}
	return n.send(ctx, name, p, msg)
}

func (n *Notifier) send(ctx context.Context, name string, p Provider, msg *Message) (*Result, error) {
	n.mu.RLock()
	hooks := n.hooks
	n.mu.RUnlock()

	start := time.Now()
	res, err := runHooked(ctx, p, hooks, msg)
	elapsed := time.Since(start)

	attrs := []any{
		slog.String("provider", name),
		slog.String("channel", string(p.Channel())),
		slog.Int("recipients", recipientCount(msg)),
		slog.Duration("duration", elapsed),
	}
	if err != nil {
		n.log.WarnContext(ctx, "notijet: send failed", append(attrs, slog.Any("error", err))...)
		return nil, err
	}
	n.log.DebugContext(ctx, "notijet: sent", append(attrs,
		slog.String("via", res.Provider),
		slog.String("message_id", res.MessageID()))...)
	return res, nil
}

func noProvider(message string) *errorx.Error {
	return providerError(errorx.BadRequestErrorType, "notifier", "", message, ErrNoProvider)
}

func recipientCount(msg *Message) int {
	if msg == nil {
		return 0
	}
	return len(msg.Recipients()) + len(CleanList(msg.CC)) + len(CleanList(msg.BCC))
}
