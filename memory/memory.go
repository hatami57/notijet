// Package memory is a notijet provider that keeps sent messages in memory
// instead of delivering them, for tests and local development:
//
//	sms := memory.New()
//	svc := NewSignupService(sms) // takes a notijet.Provider
//	svc.SendCode(ctx, "09121234567")
//	code := sms.LastTo("09121234567").Template.Get("code")
//
// It validates messages the way a real provider of its channel does, and can be
// scripted to fail (FailNext, FailWith) to exercise retry and failover paths.
package memory

import (
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/hatami57/notijet"
)

// Provider records messages instead of sending them. It accepts every feature
// its channel allows, templates included. It is safe for concurrent use.
type Provider struct {
	name    string
	channel notijet.Channel

	mu     sync.Mutex
	seq    int
	calls  int
	sent   []*notijet.Message
	queued []error
	failFn func(*notijet.Message) error
}

// Option configures a Provider.
type Option func(*Provider)

// WithName sets the provider name (default "memory").
func WithName(name string) Option {
	return func(p *Provider) { p.name = name }
}

// WithChannel sets the channel the provider reports and validates messages for
// (default notijet.ChannelSMS).
func WithChannel(ch notijet.Channel) Option {
	return func(p *Provider) { p.channel = ch }
}

// New returns an empty memory provider.
func New(opts ...Option) *Provider {
	p := &Provider{name: "memory", channel: notijet.ChannelSMS}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Provider) Name() string             { return p.name }
func (p *Provider) Channel() notijet.Channel { return p.channel }

// Send validates msg, applies any scripted failure and records a copy of it.
// The result has one receipt per recipient with IDs "<name>-1", "<name>-2" ...
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, notijet.Fault(p.name, "context done", err)
	}
	if err := notijet.Check(p.name, p.channel, msg); err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.calls++
	var failFn func(*notijet.Message) error
	if len(p.queued) > 0 {
		err := p.queued[0]
		p.queued = p.queued[1:]
		if err != nil {
			p.mu.Unlock()
			return nil, err
		}
	} else {
		failFn = p.failFn
	}
	p.mu.Unlock()
	// fn runs unlocked so it may inspect the provider (Len, Messages ...).
	if failFn != nil {
		if err := failFn(msg); err != nil {
			return nil, err
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, msg.Clone())
	res := &notijet.Result{Provider: p.name}
	for _, to := range allRecipients(msg) {
		p.seq++
		res.Receipts = append(res.Receipts, notijet.Receipt{
			To:        to,
			MessageID: p.name + "-" + strconv.Itoa(p.seq),
			Status:    "sent",
		})
	}
	return res, nil
}

func allRecipients(msg *notijet.Message) []string {
	out := msg.Recipients()
	out = append(out, notijet.CleanList(msg.CC)...)
	return append(out, notijet.CleanList(msg.BCC)...)
}

// FailNext scripts the outcome of the next sends, one per error, in order: a
// nil entry lets that send succeed. Once they are used up, sends succeed again
// (or follow FailWith).
func (p *Provider) FailNext(errs ...error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queued = append(p.queued, errs...)
}

// FailWith makes every send not covered by FailNext call fn and fail with the
// error it returns, if any. A nil fn removes the rule.
func (p *Provider) FailWith(fn func(*notijet.Message) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failFn = fn
}

// Messages returns the messages sent so far, oldest first.
func (p *Provider) Messages() []*notijet.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sent == nil {
		return nil
	}
	out := make([]*notijet.Message, len(p.sent))
	for i, msg := range p.sent {
		out[i] = msg.Clone()
	}
	return out
}

// Last returns the most recently sent message, or nil.
func (p *Provider) Last() *notijet.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sent) == 0 {
		return nil
	}
	return p.sent[len(p.sent)-1].Clone()
}

// LastTo returns the most recent message addressed to recipient (in To, CC or
// BCC), or nil.
func (p *Provider) LastTo(recipient string) *notijet.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.sent) - 1; i >= 0; i-- {
		if slices.Contains(allRecipients(p.sent[i]), recipient) {
			return p.sent[i].Clone()
		}
	}
	return nil
}

// Len returns the number of messages sent.
func (p *Provider) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

// Calls returns the number of sends attempted past validation, failed ones
// included.
func (p *Provider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// Reset forgets sent messages, counters and scripted failures.
func (p *Provider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq, p.calls = 0, 0
	p.sent, p.queued, p.failFn = nil, nil, nil
}
