package notijet

import (
	"context"
	"time"
)

// Hooks are callbacks run around a send, through WithHooks or
// Notifier.AddHooks. Either field may be nil.
type Hooks struct {
	// BeforeSend runs before the provider is called. It receives a copy of the
	// message that it may adjust — add a tag, redirect recipients in a staging
	// environment — without touching the caller's value. Returning an error
	// aborts the send with that error; return one of the notijet constructors
	// (Invalid, Rejected ...) so retry logic classifies it correctly, since a
	// plain error counts as retryable.
	BeforeSend func(ctx context.Context, provider string, msg *Message) error
	// AfterSend runs after every send, successful or not, including one that a
	// BeforeSend hook aborted. Use it for metrics, audit records or alerts.
	AfterSend func(ctx context.Context, ev SendEvent)
}

// SendEvent describes a finished send.
type SendEvent struct {
	// Provider is the Name of the provider that was called.
	Provider string
	Channel  Channel
	// Message is the message as sent, after BeforeSend hooks ran.
	Message *Message
	// Result is nil when Err is set.
	Result   *Result
	Err      error
	Duration time.Duration
}

// WithHooks wraps p so h runs around every Send. The returned provider keeps
// p's Name and Channel.
func WithHooks(p Provider, h Hooks) Provider {
	return &hooked{Provider: p, hooks: []Hooks{h}}
}

type hooked struct {
	Provider
	hooks []Hooks
}

func (h *hooked) Send(ctx context.Context, msg *Message) (*Result, error) {
	return runHooked(ctx, h.Provider, h.hooks, msg)
}

// runHooked sends msg through p with hooks around it. It also guarantees a
// non-nil Result naming the provider on success, so callers and AfterSend hooks
// need not special-case a provider that returns (nil, nil).
func runHooked(ctx context.Context, p Provider, hooks []Hooks, msg *Message) (*Result, error) {
	start := time.Now()
	finish := func(res *Result, err error) (*Result, error) {
		if err == nil {
			if res == nil {
				res = &Result{}
			}
			if res.Provider == "" {
				res.Provider = p.Name()
			}
		} else {
			res = nil
		}
		ev := SendEvent{
			Provider: p.Name(),
			Channel:  p.Channel(),
			Message:  msg,
			Result:   res,
			Err:      err,
			Duration: time.Since(start),
		}
		for _, h := range hooks {
			if h.AfterSend != nil {
				h.AfterSend(ctx, ev)
			}
		}
		return res, err
	}

	if msg != nil && hasBeforeSend(hooks) {
		msg = msg.Clone()
		for _, h := range hooks {
			if h.BeforeSend == nil {
				continue
			}
			if err := h.BeforeSend(ctx, p.Name(), msg); err != nil {
				return finish(nil, err)
			}
		}
	}
	return finish(p.Send(ctx, msg))
}

func hasBeforeSend(hooks []Hooks) bool {
	for _, h := range hooks {
		if h.BeforeSend != nil {
			return true
		}
	}
	return false
}
