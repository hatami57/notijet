package notijet_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sms(to string) *notijet.Message {
	return &notijet.Message{To: []string{to}, Text: "hello"}
}

func TestNotifierDefaultsAndNames(t *testing.T) {
	a := memory.New(memory.WithName("a"))
	b := memory.New(memory.WithName("b"))
	mail := memory.New(memory.WithName("mail"), memory.WithChannel(notijet.ChannelEmail))

	n := notijet.NewNotifier(notijet.WithLogger(nil))
	n.MustRegister("", a).MustRegister("backup", b).MustRegister("mail", mail)
	assert.Equal(t, []string{"a", "backup", "mail"}, n.Names())

	res, err := n.Send(context.Background(), notijet.ChannelSMS, sms("0912"))
	require.NoError(t, err)
	assert.Equal(t, "a", res.Provider, "first registered provider is the channel default")

	require.NoError(t, n.SetDefault("backup"))
	res, err = n.Send(context.Background(), notijet.ChannelSMS, sms("0912"))
	require.NoError(t, err)
	assert.Equal(t, "b", res.Provider)

	res, err = n.SendVia(context.Background(), "a", sms("0912"))
	require.NoError(t, err)
	assert.Equal(t, "a", res.Provider)
	assert.Equal(t, 2, a.Len())

	name, p, err := n.Default(notijet.ChannelEmail)
	require.NoError(t, err)
	assert.Equal(t, "mail", name)
	assert.Same(t, mail, p)
}

func TestNotifierErrors(t *testing.T) {
	n := notijet.NewNotifier(notijet.WithLogger(nil))
	assert.True(t, errorx.IsBadRequestError(n.Register("x", nil)))
	require.NoError(t, n.Register("x", memory.New()))
	assert.Error(t, n.Register("x", memory.New()), "duplicate name")

	_, err := n.Send(context.Background(), notijet.ChannelPush, sms("t"))
	assert.ErrorIs(t, err, notijet.ErrNoProvider)
	_, err = n.SendVia(context.Background(), "missing", sms("t"))
	assert.ErrorIs(t, err, notijet.ErrNoProvider)
	assert.ErrorIs(t, n.SetDefault("missing"), notijet.ErrNoProvider)
	assert.False(t, notijet.IsRetryable(err))
	assert.Panics(t, func() { n.MustRegister("x", memory.New()) })
}

func TestNotifierHooksAndLogging(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := memory.New()
	n := notijet.NewNotifier(notijet.WithLogger(log))
	n.MustRegister("otp", p)

	var events []notijet.SendEvent
	n.AddHooks(notijet.Hooks{
		BeforeSend: func(_ context.Context, provider string, msg *notijet.Message) error {
			assert.Equal(t, "memory", provider)
			msg.Metadata = map[string]string{"env": "test"}
			return nil
		},
		AfterSend: func(_ context.Context, ev notijet.SendEvent) { events = append(events, ev) },
	})

	orig := sms("09121234567")
	_, err := n.Send(context.Background(), notijet.ChannelSMS, orig)
	require.NoError(t, err)
	assert.Nil(t, orig.Metadata, "hooks adjust a copy")
	assert.Equal(t, "test", p.Last().Metadata["env"])
	require.Len(t, events, 1)
	assert.Equal(t, "memory", events[0].Provider)
	assert.NoError(t, events[0].Err)
	assert.Equal(t, "memory-1", events[0].Result.MessageID())
	assert.Contains(t, buf.String(), "notijet: sent")
	assert.Contains(t, buf.String(), "provider=otp")
	assert.NotContains(t, buf.String(), "09121234567", "recipients are not logged")

	p.FailNext(notijet.Rejected("memory", "418", "no credit", notijet.ErrInsufficientCredit))
	_, err = n.Send(context.Background(), notijet.ChannelSMS, sms("0912"))
	assert.ErrorIs(t, err, notijet.ErrInsufficientCredit)
	require.Len(t, events, 2)
	assert.ErrorIs(t, events[1].Err, notijet.ErrInsufficientCredit)
	assert.Nil(t, events[1].Result)
	assert.Contains(t, buf.String(), "notijet: send failed")
}

func TestWithHooksBeforeSendAborts(t *testing.T) {
	p := memory.New()
	stop := notijet.Invalid("policy", "quiet hours")
	var after error
	h := notijet.WithHooks(p, notijet.Hooks{
		BeforeSend: func(context.Context, string, *notijet.Message) error { return stop },
		AfterSend:  func(_ context.Context, ev notijet.SendEvent) { after = ev.Err },
	})
	assert.Equal(t, "memory", h.Name())
	assert.Equal(t, notijet.ChannelSMS, h.Channel())
	_, err := h.Send(context.Background(), sms("0912"))
	assert.Same(t, stop, err)
	assert.Same(t, stop, after)
	assert.Equal(t, 0, p.Calls())
}

func TestFailover(t *testing.T) {
	a := memory.New(memory.WithName("a"))
	b := memory.New(memory.WithName("b"))
	c := memory.New(memory.WithName("c"))
	f := notijet.Failover(a, b, c)
	assert.Equal(t, "failover", f.Name())
	assert.Equal(t, notijet.ChannelSMS, f.Channel())

	a.FailNext(notijet.Rejected("a", "418", "", notijet.ErrInsufficientCredit))
	res, err := f.Send(context.Background(), sms("0912"))
	require.NoError(t, err)
	assert.Equal(t, "b", res.Provider)
	assert.Equal(t, 0, a.Len())
	assert.Equal(t, 1, b.Len())

	// Every provider fails: the representative error is the first retryable one.
	a.FailNext(notijet.Rejected("a", "401", "", notijet.ErrUnauthorized))
	b.FailNext(notijet.Fault("b", "timeout", nil))
	c.FailNext(notijet.Unsupported("c", "Template"))
	_, err = f.Send(context.Background(), sms("0912"))
	var fe *notijet.FailoverError
	require.ErrorAs(t, err, &fe)
	require.Len(t, fe.Errors, 3)
	assert.ErrorIs(t, fe.Errors[0], notijet.ErrUnauthorized)
	assert.True(t, notijet.IsRetryable(err))
	assert.True(t, errorx.IsInternalError(err))
	assert.Equal(t, "b", errorx.GetError(err).Subject)
	assert.Contains(t, err.Error(), "all 3 providers failed")

	// All permanent: the last error represents the chain.
	a.FailNext(notijet.Rejected("a", "401", "", notijet.ErrUnauthorized))
	b.FailNext(notijet.Rejected("b", "418", "", notijet.ErrInsufficientCredit))
	c.FailNext(notijet.Unsupported("c", "Template"))
	_, err = f.Send(context.Background(), sms("0912"))
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	assert.False(t, notijet.IsRetryable(err))

	// A single failure is returned as is.
	one := notijet.Failover(a)
	a.FailNext(notijet.Fault("a", "down", nil))
	_, err = one.Send(context.Background(), sms("0912"))
	assert.False(t, errors.As(err, &fe))

	assert.Panics(t, func() { notijet.Failover() })
	assert.Panics(t, func() { notijet.Failover(a, nil) })
}

func TestFailoverStopsWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := memory.New(memory.WithName("a"))
	b := memory.New(memory.WithName("b"))
	a.FailWith(func(*notijet.Message) error {
		cancel()
		return notijet.Fault("a", "cancelled mid-send", context.Canceled)
	})
	_, err := notijet.Failover(a, b).Send(ctx, sms("0912"))
	assert.Error(t, err)
	assert.Equal(t, 0, b.Calls())
}

func TestRetry(t *testing.T) {
	p := memory.New()
	r := notijet.Retry(p, notijet.RetryAttempts(3), notijet.RetryBackoff(0, 0))
	assert.Equal(t, "memory", r.Name())

	p.FailNext(notijet.Fault("memory", "down", nil), notijet.Throttled("memory", "429", ""))
	res, err := r.Send(context.Background(), sms("0912"))
	require.NoError(t, err)
	assert.Equal(t, "memory", res.Provider)
	assert.Equal(t, 3, p.Calls())

	// Permanent errors are not retried.
	p.Reset()
	p.FailNext(notijet.Rejected("memory", "401", "", notijet.ErrUnauthorized))
	_, err = r.Send(context.Background(), sms("0912"))
	assert.ErrorIs(t, err, notijet.ErrUnauthorized)
	assert.Equal(t, 1, p.Calls())

	// Attempts run out: the last error is returned.
	p.Reset()
	last := notijet.Fault("memory", "third", nil)
	p.FailNext(notijet.Fault("memory", "first", nil), notijet.Fault("memory", "second", nil), last)
	_, err = r.Send(context.Background(), sms("0912"))
	assert.Same(t, last, err)
	assert.Equal(t, 3, p.Calls())

	// RetryIf overrides the test.
	p.Reset()
	r = notijet.Retry(p, notijet.RetryBackoff(0, 0), notijet.RetryIf(func(error) bool { return true }))
	p.FailNext(notijet.Rejected("memory", "x", "", nil))
	_, err = r.Send(context.Background(), sms("0912"))
	require.NoError(t, err)
	assert.Equal(t, 2, p.Calls())
}

func TestRetryHonoursContextDuringBackoff(t *testing.T) {
	p := memory.New()
	p.FailWith(func(*notijet.Message) error { return notijet.Fault("memory", "down", nil) })
	r := notijet.Retry(p, notijet.RetryAttempts(5), notijet.RetryBackoff(time.Hour, time.Hour))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Send(ctx, sms("0912"))
	assert.Error(t, err)
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, 1, p.Calls())
}

func TestNotifierConcurrent(t *testing.T) {
	p := memory.New()
	n := notijet.NewNotifier(notijet.WithLogger(nil))
	n.MustRegister("", p)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			_, err := n.Send(context.Background(), notijet.ChannelSMS, sms("0912"))
			assert.NoError(t, err)
		})
		wg.Go(func() { n.AddHooks(notijet.Hooks{}) })
	}
	wg.Wait()
	assert.Equal(t, 50, p.Len())
}
