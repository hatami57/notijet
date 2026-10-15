package notijet

import (
	"context"
	"math/rand/v2"
	"time"
)

// Retry defaults.
const (
	DefaultRetryAttempts = 3
	DefaultRetryInitial  = time.Second
	DefaultRetryMax      = 30 * time.Second
)

// RetryOption configures Retry.
type RetryOption func(*retry)

// RetryAttempts sets the total number of attempts, the first included. Values
// below 1 are treated as 1. The default is DefaultRetryAttempts.
func RetryAttempts(n int) RetryOption {
	return func(r *retry) { r.attempts = max(n, 1) }
}

// RetryBackoff sets the delay before the second attempt and the cap the delay
// doubles up to. Each delay is jittered to between half and all of its value,
// so many senders failing together do not retry in lockstep. The defaults are
// DefaultRetryInitial and DefaultRetryMax.
func RetryBackoff(initial, maxDelay time.Duration) RetryOption {
	return func(r *retry) {
		r.initial = max(initial, 0)
		r.max = max(maxDelay, r.initial)
	}
}

// RetryIf sets the test deciding whether an error is worth another attempt. The
// default is IsRetryable.
func RetryIf(fn func(error) bool) RetryOption {
	return func(r *retry) {
		if fn != nil {
			r.retryable = fn
		}
	}
}

// Retry returns a provider that sends through p and, when the send fails with a
// retryable error (see IsRetryable), tries again with exponential backoff. It
// stops when ctx is done and returns the last error. The returned provider
// keeps p's Name and Channel.
//
// A provider that reports success only after the platform accepted the message
// makes retries safe for faults before that point, but a timeout after the
// platform accepted it can still cause a duplicate; set Message.ID where the
// provider forwards it, so the platform or your records can spot one.
func Retry(p Provider, opts ...RetryOption) Provider {
	r := &retry{
		Provider:  p,
		attempts:  DefaultRetryAttempts,
		initial:   DefaultRetryInitial,
		max:       DefaultRetryMax,
		retryable: IsRetryable,
		sleep:     sleepCtx,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

type retry struct {
	Provider
	attempts  int
	initial   time.Duration
	max       time.Duration
	retryable func(error) bool
	sleep     func(ctx context.Context, d time.Duration) error
}

func (r *retry) Send(ctx context.Context, msg *Message) (*Result, error) {
	var err error
	for attempt := 1; ; attempt++ {
		var res *Result
		res, err = r.Provider.Send(ctx, msg)
		if err == nil {
			return res, nil
		}
		if attempt >= r.attempts || !r.retryable(err) || ctx.Err() != nil {
			return nil, err
		}
		if r.sleep(ctx, r.delay(attempt)) != nil {
			return nil, err
		}
	}
}

// delay returns the jittered wait after the given failed attempt (1-based).
func (r *retry) delay(attempt int) time.Duration {
	d := r.initial
	for i := 1; i < attempt && d < r.max; i++ {
		d *= 2
	}
	d = min(d, r.max)
	if d <= 0 {
		return 0
	}
	half := d / 2
	return half + rand.N(d-half+1)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
