package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hatami57/microjet/gormx"
	"github.com/hatami57/microjet/gormx/sqlite"
	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// clock is a settable test clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

func openDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := sqlite.Driver().Open(gormx.Config{Name: name}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	return db
}

type fixture struct {
	db    *gorm.DB
	store *DBStore
	sms   *memory.Provider
	mail  *memory.Provider
	n     *notijet.Notifier
	clock *clock
	q     *Queue
}

func setup(t *testing.T, opts ...Option) *fixture {
	t.Helper()
	f := &fixture{db: openDB(t, ":memory:"), clock: &clock{now: t0}}
	f.store = NewDBStore(f.db)
	require.NoError(t, f.store.Migrate())
	f.sms = memory.New()
	f.mail = memory.New(memory.WithName("mail"), memory.WithChannel(notijet.ChannelEmail))
	f.n = notijet.NewNotifier(notijet.WithLogger(nil))
	f.n.MustRegister("otp", f.sms).MustRegister("mail", f.mail)
	base := []Option{WithClock(f.clock.Now), WithLogger(slog.New(slog.DiscardHandler)), Backoff(time.Minute, time.Hour)}
	f.q = New(f.store, f.n, append(base, opts...)...)
	return f
}

func sms(to string) *notijet.Message { return &notijet.Message{To: []string{to}, Text: "hello"} }

func TestEnqueueAndDeliver(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id, err := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"))
	require.NoError(t, err)
	assert.NotEmpty(t, id)

	rec, err := f.q.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, StatusQueued, rec.Status)
	assert.Equal(t, "sms", rec.Channel)
	assert.False(t, rec.Done())

	n, err := f.q.Process(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, id, f.sms.Last().ID, "the generated ID travels with the message")

	rec, _ = f.q.Get(ctx, id)
	assert.Equal(t, StatusSent, rec.Status)
	assert.True(t, rec.Done())
	assert.Equal(t, 1, rec.Attempts)
	assert.Equal(t, "memory", rec.SentVia)
	assert.Equal(t, t0, rec.SentAt.UTC())
	assert.Nil(t, rec.LockedUntil)
	assert.Empty(t, rec.ClaimToken)
	receipts, err := rec.DecodeReceipts()
	require.NoError(t, err)
	assert.Equal(t, "memory-1", receipts[0].MessageID)

	n, err = f.q.Process(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left")
}

func TestEnqueueIsIdempotentByID(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	msg := sms("0912")
	msg.ID = "order-42-shipped"
	for range 3 {
		id, err := f.q.Enqueue(ctx, notijet.ChannelSMS, msg)
		require.NoError(t, err)
		assert.Equal(t, "order-42-shipped", id)
	}
	_, err := f.q.Process(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, f.sms.Len())
}

func TestEnqueueValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, err := f.q.Enqueue(ctx, notijet.ChannelSMS, nil)
	assert.Error(t, err)
	_, err = f.q.Enqueue(ctx, notijet.ChannelPush, sms("x"))
	assert.ErrorIs(t, err, notijet.ErrNoProvider)
	_, err = f.q.Enqueue(ctx, notijet.ChannelSMS, sms("x"), Via("nope"))
	assert.ErrorIs(t, err, notijet.ErrNoProvider)
	_, err = f.q.Enqueue(ctx, notijet.ChannelSMS, &notijet.Message{Data: map[string]string{"bad": string([]byte{0xff})}})
	assert.NoError(t, err, "invalid UTF-8 is replaced by encoding/json, not an error")
}

func TestViaAndAt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	mail := &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"}
	id, err := f.q.Enqueue(ctx, notijet.ChannelSMS, mail, Via("mail"), At(t0.Add(time.Hour)))
	require.NoError(t, err)
	rec, _ := f.q.Get(ctx, id)
	assert.Equal(t, "email", rec.Channel, "the channel follows the Via provider")
	assert.Equal(t, "mail", rec.Provider)

	n, _ := f.q.Process(ctx)
	assert.Zero(t, n, "not due yet")
	f.clock.Add(time.Hour)
	n, err = f.q.Process(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, f.mail.Len())
}

func TestRetryWithBackoffThenSuccess(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.sms.FailNext(notijet.Fault("memory", "down", nil))
	id, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"))

	n, err := f.q.Process(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	rec, _ := f.q.Get(ctx, id)
	assert.Equal(t, StatusQueued, rec.Status)
	assert.Equal(t, 1, rec.Attempts)
	assert.Contains(t, rec.LastError, "down")
	wait := rec.NextAttemptAt.Sub(t0)
	assert.True(t, wait >= 30*time.Second && wait <= time.Minute, "first backoff is ½–1× the initial delay, got %v", wait)

	n, _ = f.q.Process(ctx)
	assert.Zero(t, n, "not due before the backoff")
	f.clock.Add(time.Minute)
	n, err = f.q.Process(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	rec, _ = f.q.Get(ctx, id)
	assert.Equal(t, StatusSent, rec.Status)
	assert.Equal(t, 2, rec.Attempts)
	assert.Empty(t, rec.LastError, "a success clears the last error")
}

func TestMaxAttemptsThenManualRetry(t *testing.T) {
	f := setup(t, MaxAttempts(2))
	ctx := context.Background()
	f.sms.FailWith(func(*notijet.Message) error { return notijet.Throttled("memory", "429", "") })
	id, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"))
	for range 2 {
		_, err := f.q.Process(ctx)
		require.NoError(t, err)
		f.clock.Add(time.Hour)
	}
	rec, _ := f.q.Get(ctx, id)
	assert.Equal(t, StatusFailed, rec.Status)
	assert.Equal(t, 2, rec.Attempts)
	assert.Equal(t, "429", rec.ErrorCode)

	f.sms.FailWith(nil)
	ok, err := f.q.Retry(ctx, id)
	require.NoError(t, err)
	assert.True(t, ok)
	n, _ := f.q.Process(ctx)
	assert.Equal(t, 1, n)
	ok, err = f.q.Retry(ctx, id)
	require.NoError(t, err)
	assert.False(t, ok, "a sent message cannot be requeued")
}

func TestPermanentFailure(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.sms.FailNext(notijet.Rejected("memory", "418", "no credit", notijet.ErrInsufficientCredit))
	id, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"))
	_, err := f.q.Process(ctx)
	require.NoError(t, err)
	rec, _ := f.q.Get(ctx, id)
	assert.Equal(t, StatusFailed, rec.Status)
	assert.Equal(t, 1, rec.Attempts)
	assert.Equal(t, "418", rec.ErrorCode)
}

func TestPartialIsNeverResent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.sms.FailNext(&notijet.PartialError{
		Result: &notijet.Result{Provider: "memory", Receipts: []notijet.Receipt{{To: "a", MessageID: "1"}}},
		Err:    notijet.Fault("memory", "down", nil),
	})
	id, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"))
	_, err := f.q.Process(ctx)
	require.NoError(t, err)
	rec, _ := f.q.Get(ctx, id)
	assert.Equal(t, StatusPartial, rec.Status)
	assert.Equal(t, "memory", rec.SentVia)
	receipts, _ := rec.DecodeReceipts()
	assert.Equal(t, "1", receipts[0].MessageID)
	f.clock.Add(24 * time.Hour)
	n, _ := f.q.Process(ctx)
	assert.Zero(t, n)
}

func TestUnreadableMessageFails(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, err := f.store.Insert(ctx, &Record{ID: "bad", Channel: "sms", Message: []byte("{"), Status: StatusQueued, NextAttemptAt: t0, CreatedAt: t0, UpdatedAt: t0})
	require.NoError(t, err)
	_, err = f.q.Process(ctx)
	require.NoError(t, err)
	rec, _ := f.q.Get(ctx, "bad")
	assert.Equal(t, StatusFailed, rec.Status)
	assert.Contains(t, rec.LastError, "unreadable")
	_, err = rec.Decode()
	assert.Error(t, err)
}

func TestEnqueueJoinsTransaction(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	repo := gormx.NewBaseRepository(f.db)
	boom := errors.New("order insert failed")

	err := repo.RunTx(ctx, func(ctx context.Context) error {
		msg := sms("0912")
		msg.ID = "rolled-back"
		if _, err := f.q.Enqueue(ctx, notijet.ChannelSMS, msg); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	rec, err := f.q.Get(ctx, "rolled-back")
	require.NoError(t, err)
	assert.Nil(t, rec, "the message rolled back with the transaction")

	err = repo.RunTx(ctx, func(ctx context.Context) error {
		msg := sms("0912")
		msg.ID = "committed"
		_, err := f.q.Enqueue(ctx, notijet.ChannelSMS, msg)
		return err
	})
	require.NoError(t, err)
	rec, _ = f.q.Get(ctx, "committed")
	assert.NotNil(t, rec)
}

func TestLeaseExpiryAndLostClaim(t *testing.T) {
	f := setup(t, Lease(time.Minute))
	ctx := context.Background()
	id, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"))

	// A worker claims the message and dies.
	claimed, err := f.store.Claim(ctx, t0, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	stale := claimed[0]
	assert.Equal(t, StatusSending, stale.Status)

	n, _ := f.q.Process(ctx)
	assert.Zero(t, n, "still leased")
	f.clock.Add(2 * time.Minute)
	n, err = f.q.Process(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "taken over once the lease expired")
	rec, _ := f.q.Get(ctx, id)
	assert.Equal(t, 2, rec.Attempts)

	// The dead worker's late outcome is ignored.
	stale.Status, stale.LastError = StatusFailed, "late"
	ok, err := f.store.Finish(ctx, stale)
	require.NoError(t, err)
	assert.False(t, ok)
	rec, _ = f.q.Get(ctx, id)
	assert.Equal(t, StatusSent, rec.Status)
}

func TestReplicasNeverDoubleSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db") + "?_pragma=busy_timeout(10000)"
	ctx := context.Background()
	var delivered atomic.Int32
	counting := &countingProvider{n: &delivered}

	newReplica := func() *Queue {
		db := openDB(t, path)
		store := NewDBStore(db)
		require.NoError(t, store.Migrate())
		n := notijet.NewNotifier(notijet.WithLogger(nil))
		n.MustRegister("", counting)
		return New(store, n, BatchSize(7), Workers(3), WithLogger(slog.New(slog.DiscardHandler)))
	}
	a, b := newReplica(), newReplica()
	const total = 60
	for i := range total {
		_, err := a.Enqueue(ctx, notijet.ChannelSMS, sms(fmt.Sprint(i)))
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	for _, q := range []*Queue{a, b} {
		wg.Go(func() {
			for {
				n, err := q.Process(ctx)
				assert.NoError(t, err)
				if n == 0 {
					return
				}
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(total), delivered.Load(), "every message delivered exactly once")
}

type countingProvider struct{ n *atomic.Int32 }

func (c *countingProvider) Name() string             { return "count" }
func (c *countingProvider) Channel() notijet.Channel { return notijet.ChannelSMS }
func (c *countingProvider) Send(context.Context, *notijet.Message) (*notijet.Result, error) {
	c.n.Add(1)
	return &notijet.Result{Provider: "count"}, nil
}

func TestRetention(t *testing.T) {
	f := setup(t, Retention(time.Hour))
	ctx := context.Background()
	sent, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("1"))
	_, err := f.q.Process(ctx)
	require.NoError(t, err)
	f.clock.Add(30 * time.Minute)
	pending, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("2"), At(t0.Add(48*time.Hour)))
	_, err = f.q.Process(ctx)
	require.NoError(t, err)
	rec, _ := f.q.Get(ctx, sent)
	assert.NotNil(t, rec, "kept within the retention window")

	f.clock.Add(time.Hour)
	_, err = f.q.Process(ctx)
	require.NoError(t, err)
	rec, _ = f.q.Get(ctx, sent)
	assert.Nil(t, rec, "pruned")
	rec, _ = f.q.Get(ctx, pending)
	assert.NotNil(t, rec, "queued messages are never pruned")
}

func TestShutdownLeavesMessageClaimed(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.sms.FailWith(func(*notijet.Message) error {
		cancel()
		return notijet.Fault("memory", "cancelled", context.Canceled)
	})
	id, _ := f.q.Enqueue(context.Background(), notijet.ChannelSMS, sms("0912"))
	n, err := f.q.Process(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	rec, _ := f.q.Get(context.Background(), id)
	assert.Equal(t, StatusSending, rec.Status, "left for the lease to expire")
	assert.Empty(t, rec.LastError)
}

func TestRunWakesOnEnqueueAndStops(t *testing.T) {
	f := setup(t, Interval(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- f.q.Run(ctx) }()

	_, err := f.q.Enqueue(context.Background(), notijet.ChannelSMS, sms("0912"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.sms.Len() == 1 }, 5*time.Second, 10*time.Millisecond,
		"an enqueue wakes the worker without waiting for the interval")
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestStoreErrorsSurface(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sqlDB, err := f.db.DB()
	require.NoError(t, err)
	sqlDB.Close()

	_, err = f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"))
	assert.Error(t, err)
	_, err = f.q.Process(ctx)
	assert.Error(t, err)

	logged := make(chan struct{}, 1)
	q := New(f.store, f.n, WithLogger(slog.New(handlerFunc(func() {
		select {
		case logged <- struct{}{}:
		default:
		}
	}))))
	runCtx, cancel := context.WithCancel(ctx)
	go q.Run(runCtx)
	select {
	case <-logged:
	case <-time.After(5 * time.Second):
		t.Fatal("a failing pass was not logged")
	}
	cancel()
}

// handlerFunc is a slog handler that calls fn for every record.
type handlerFunc func()

func (h handlerFunc) Enabled(context.Context, slog.Level) bool  { return true }
func (h handlerFunc) Handle(context.Context, slog.Record) error { h(); return nil }
func (h handlerFunc) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h handlerFunc) WithGroup(string) slog.Handler             { return h }

func TestBackoffBoundsAndOptions(t *testing.T) {
	q := New(nil, nil, Backoff(time.Second, 4*time.Second), BatchSize(-1), Workers(0), Lease(0), MaxAttempts(0), Interval(0), Retention(-time.Hour), WithClock(nil), WithLogger(nil))
	for attempt, hi := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 9: 4 * time.Second} {
		for range 50 {
			d := q.backoff(attempt)
			assert.True(t, d >= hi/2 && d <= hi, "attempt %d: %v", attempt, d)
		}
	}
	assert.Equal(t, DefaultBatchSize, q.batch)
	assert.Equal(t, 1, q.workers)
	assert.Equal(t, DefaultLease, q.lease)
	assert.Equal(t, DefaultMaxAttempts, q.maxAttempts)
	assert.Equal(t, DefaultInterval, q.interval)
	assert.Zero(t, q.retention)
	assert.NotNil(t, q.now)
	assert.NotNil(t, q.log)
}

func TestCancelScheduledMessage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	msg := sms("0912")
	msg.ID = "reminder-42"
	_, err := f.q.Enqueue(ctx, notijet.ChannelSMS, msg, At(t0.Add(24*time.Hour)))
	require.NoError(t, err)

	f.clock.Add(time.Hour)
	ok, err := f.q.Cancel(ctx, "reminder-42")
	require.NoError(t, err)
	assert.True(t, ok)
	rec, _ := f.q.Get(ctx, "reminder-42")
	assert.Equal(t, StatusCancelled, rec.Status)
	assert.True(t, rec.Done())
	assert.Equal(t, t0.Add(time.Hour), rec.UpdatedAt.UTC())

	f.clock.Add(48 * time.Hour)
	n, err := f.q.Process(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Zero(t, f.sms.Len(), "a cancelled message is never sent")

	ok, err = f.q.Cancel(ctx, "reminder-42")
	require.NoError(t, err)
	assert.False(t, ok, "already cancelled")
	ok, err = f.q.Retry(ctx, "reminder-42")
	require.NoError(t, err)
	assert.False(t, ok, "Retry does not revive a cancelled message")
	_, err = f.q.Enqueue(ctx, notijet.ChannelSMS, msg)
	require.NoError(t, err)
	rec, _ = f.q.Get(ctx, "reminder-42")
	assert.Equal(t, StatusCancelled, rec.Status, "enqueueing the same ID again is still a no-op")
}

func TestCancelMessageWaitingForRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.sms.FailNext(notijet.Fault("memory", "down", nil))
	id, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"))
	_, err := f.q.Process(ctx)
	require.NoError(t, err)
	rec, _ := f.q.Get(ctx, id)
	require.Equal(t, StatusQueued, rec.Status)

	ok, err := f.q.Cancel(ctx, id)
	require.NoError(t, err)
	assert.True(t, ok)
	f.clock.Add(time.Hour)
	n, _ := f.q.Process(ctx)
	assert.Zero(t, n)
	rec, _ = f.q.Get(ctx, id)
	assert.Equal(t, StatusCancelled, rec.Status)
	assert.Equal(t, 1, rec.Attempts)
}

func TestCancelOnlyQueuedMessages(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	ok, err := f.q.Cancel(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, ok)

	sent, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("1"))
	_, err = f.q.Process(ctx)
	require.NoError(t, err)
	ok, err = f.q.Cancel(ctx, sent)
	require.NoError(t, err)
	assert.False(t, ok, "a sent message cannot be cancelled")

	inFlight, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("2"))
	claimed, err := f.store.Claim(ctx, t0, time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	ok, err = f.q.Cancel(ctx, inFlight)
	require.NoError(t, err)
	assert.False(t, ok, "a message being sent cannot be cancelled")
	rec, _ := f.q.Get(ctx, inFlight)
	assert.Equal(t, StatusSending, rec.Status)
}

func TestCancelJoinsTransaction(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	repo := gormx.NewBaseRepository(f.db)
	boom := errors.New("booking update failed")
	id, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("0912"), At(t0.Add(time.Hour)))

	err := repo.RunTx(ctx, func(ctx context.Context) error {
		if ok, err := f.q.Cancel(ctx, id); err != nil || !ok {
			return fmt.Errorf("cancel: %v %v", ok, err)
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	rec, _ := f.q.Get(ctx, id)
	assert.Equal(t, StatusQueued, rec.Status, "the cancellation rolled back with the transaction")

	err = repo.RunTx(ctx, func(ctx context.Context) error {
		_, err := f.q.Cancel(ctx, id)
		return err
	})
	require.NoError(t, err)
	rec, _ = f.q.Get(ctx, id)
	assert.Equal(t, StatusCancelled, rec.Status)
}

// TestQueueJoinsGormTransactionViaWithTx covers apps that open transactions
// with gorm directly: gormx.WithTx puts the transaction in the context, and
// Enqueue and Cancel join it.
func TestQueueJoinsGormTransactionViaWithTx(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	boom := errors.New("rollback")
	scheduled, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("1"), At(t0.Add(time.Hour)))

	err := f.db.Transaction(func(tx *gorm.DB) error {
		ctx := gormx.WithTx(ctx, tx)
		msg := sms("2")
		msg.ID = "in-tx"
		if _, err := f.q.Enqueue(ctx, notijet.ChannelSMS, msg); err != nil {
			return err
		}
		if _, err := f.q.Cancel(ctx, scheduled); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	rec, _ := f.q.Get(ctx, "in-tx")
	assert.Nil(t, rec, "the enqueue rolled back with the gorm transaction")
	rec, _ = f.q.Get(ctx, scheduled)
	assert.Equal(t, StatusQueued, rec.Status, "so did the cancellation")

	err = f.db.Transaction(func(tx *gorm.DB) error {
		ctx := gormx.WithTx(ctx, tx)
		msg := sms("2")
		msg.ID = "in-tx"
		_, err := f.q.Enqueue(ctx, notijet.ChannelSMS, msg)
		return err
	})
	require.NoError(t, err)
	rec, _ = f.q.Get(ctx, "in-tx")
	assert.NotNil(t, rec, "committed with the gorm transaction")
}

func TestRetentionPrunesCancelled(t *testing.T) {
	f := setup(t, Retention(time.Hour))
	ctx := context.Background()
	id, _ := f.q.Enqueue(ctx, notijet.ChannelSMS, sms("1"), At(t0.Add(48*time.Hour)))
	ok, err := f.q.Cancel(ctx, id)
	require.NoError(t, err)
	require.True(t, ok)
	f.clock.Add(2 * time.Hour)
	_, err = f.q.Process(ctx)
	require.NoError(t, err)
	rec, _ := f.q.Get(ctx, id)
	assert.Nil(t, rec, "pruned")
}

func TestCancelStoreError(t *testing.T) {
	f := setup(t)
	sqlDB, err := f.db.DB()
	require.NoError(t, err)
	sqlDB.Close()
	ok, err := f.q.Cancel(context.Background(), "x")
	assert.Error(t, err)
	assert.False(t, ok)
}
