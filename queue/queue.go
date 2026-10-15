package queue

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/notijet"
)

// Defaults for the Queue options.
const (
	DefaultInterval       = 5 * time.Second
	DefaultBatchSize      = 50
	DefaultLease          = 5 * time.Minute
	DefaultMaxAttempts    = 10
	DefaultBackoffInitial = 30 * time.Second
	DefaultBackoffMax     = time.Hour
)

// Name is the subject of the errors the package returns.
const Name = "queue"

// Queue enqueues messages into a Store and delivers them through a Notifier.
// It is safe for concurrent use.
type Queue struct {
	store    Store
	notifier *notijet.Notifier

	interval       time.Duration
	batch          int
	workers        int
	lease          time.Duration
	maxAttempts    int
	backoffInitial time.Duration
	backoffMax     time.Duration
	retention      time.Duration
	now            func() time.Time
	log            *slog.Logger

	wake chan struct{}
}

// Option configures a Queue.
type Option func(*Queue)

// Interval sets how often Run polls for due messages (default
// DefaultInterval). An Enqueue in the same process wakes it at once.
func Interval(d time.Duration) Option {
	return func(q *Queue) {
		if d > 0 {
			q.interval = d
		}
	}
}

// BatchSize sets how many messages a pass claims (default DefaultBatchSize).
func BatchSize(n int) Option {
	return func(q *Queue) {
		if n > 0 {
			q.batch = n
		}
	}
}

// Workers sets how many messages of a batch are delivered in parallel
// (default 1).
func Workers(n int) Option {
	return func(q *Queue) {
		if n > 0 {
			q.workers = n
		}
	}
}

// Lease sets how long a claimed message stays reserved for its worker
// (default DefaultLease). A delivery is cancelled before its lease runs out,
// so another worker never sends a message that is still in flight; keep it
// well above your providers' timeouts.
func Lease(d time.Duration) Option {
	return func(q *Queue) {
		if d > 0 {
			q.lease = d
		}
	}
}

// MaxAttempts sets how many times a message is tried before it is marked
// failed (default DefaultMaxAttempts).
func MaxAttempts(n int) Option {
	return func(q *Queue) {
		if n > 0 {
			q.maxAttempts = n
		}
	}
}

// Backoff sets the delay before the second attempt and the cap it doubles up
// to (defaults DefaultBackoffInitial and DefaultBackoffMax); each delay is
// jittered to between half and all of its value.
func Backoff(initial, maxDelay time.Duration) Option {
	return func(q *Queue) {
		if initial > 0 {
			q.backoffInitial = initial
		}
		q.backoffMax = max(maxDelay, q.backoffInitial)
	}
}

// Retention deletes sent, partial and failed messages last updated longer
// than d ago, at the end of each pass. Zero (the default) keeps them.
func Retention(d time.Duration) Option {
	return func(q *Queue) { q.retention = max(d, 0) }
}

// WithClock sets the time source (default time.Now), for tests.
func WithClock(now func() time.Time) Option {
	return func(q *Queue) {
		if now != nil {
			q.now = now
		}
	}
}

// WithLogger sets the logger for delivery outcomes (default slog.Default()).
func WithLogger(l *slog.Logger) Option {
	return func(q *Queue) {
		if l != nil {
			q.log = l
		}
	}
}

// New returns a Queue storing messages in store and delivering them through
// n.
func New(store Store, n *notijet.Notifier, opts ...Option) *Queue {
	q := &Queue{
		store:          store,
		notifier:       n,
		interval:       DefaultInterval,
		batch:          DefaultBatchSize,
		workers:        1,
		lease:          DefaultLease,
		maxAttempts:    DefaultMaxAttempts,
		backoffInitial: DefaultBackoffInitial,
		backoffMax:     DefaultBackoffMax,
		now:            time.Now,
		log:            slog.Default(),
		wake:           make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(q)
	}
	return q
}

// EnqueueOption adjusts one Enqueue.
type EnqueueOption func(*enqueue)

type enqueue struct {
	provider string
	at       time.Time
}

// Via sends the message through the provider registered under name instead
// of the channel's default.
func Via(name string) EnqueueOption { return func(e *enqueue) { e.provider = name } }

// At delays delivery until t.
func At(t time.Time) EnqueueOption { return func(e *enqueue) { e.at = t } }

// Enqueue stores msg for delivery through the default provider of ch (or the
// Via provider) and returns its ID: msg.ID when set, otherwise a generated
// one. Called with a context carrying a gormx transaction, the default store
// joins it, so the message is sent only if that transaction commits.
// Enqueueing an ID that is already queued is a no-op that returns the ID.
//
// The provider must be registered in the Notifier now; the message itself is
// validated by the provider at delivery.
func (q *Queue) Enqueue(ctx context.Context, ch notijet.Channel, msg *notijet.Message, opts ...EnqueueOption) (string, error) {
	var e enqueue
	for _, opt := range opts {
		opt(&e)
	}
	if msg == nil {
		return "", notijet.Invalid(Name, "message is nil")
	}
	if e.provider != "" {
		p, err := q.notifier.Provider(e.provider)
		if err != nil {
			return "", err
		}
		ch = p.Channel()
	} else if _, _, err := q.notifier.Default(ch); err != nil {
		return "", err
	}

	id := strings.TrimSpace(msg.ID)
	if id == "" {
		id = rand.Text()
		msg = msg.Clone()
		msg.ID = id
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return "", notijet.Invalid(Name, "encoding message: "+err.Error())
	}
	now := q.now().UTC()
	at := now
	if !e.at.IsZero() {
		at = e.at.UTC()
	}
	rec := &Record{
		ID: id, Channel: string(ch), Provider: e.provider, Message: body,
		Status: StatusQueued, NextAttemptAt: at, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := q.store.Insert(ctx, rec); err != nil {
		return "", errorx.NewInternalError(Name, "storing message failed").WithInner(err)
	}
	q.signal()
	return id, nil
}

// signal wakes Run for a prompt pass. It never blocks; a pending wake-up
// already covers this one.
func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Get returns the record of the message with id, or nil when there is none.
func (q *Queue) Get(ctx context.Context, id string) (*Record, error) {
	return q.store.Get(ctx, id)
}

// Retry requeues a failed or partial message for immediate delivery with its
// attempts reset. It reports whether there was such a message. Resending a
// partial message reaches the recipients that already got it again.
func (q *Queue) Retry(ctx context.Context, id string) (bool, error) {
	ok, err := q.store.Requeue(ctx, id, q.now().UTC())
	if ok {
		q.signal()
	}
	return ok, err
}

// Run delivers due messages until ctx is cancelled: a pass at once, then one
// every Interval or as soon as something is enqueued. Pass errors are logged
// and never stop it.
func (q *Queue) Run(ctx context.Context) error {
	t := time.NewTicker(q.interval)
	defer t.Stop()
	for {
		if _, err := q.Process(ctx); err != nil && ctx.Err() == nil {
			q.log.ErrorContext(ctx, "notijet queue: pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-q.wake:
		}
	}
}

// Process runs one pass: it claims a batch of due messages, delivers them,
// records each outcome and prunes old records (see Retention). It returns how
// many messages it delivered successfully.
func (q *Queue) Process(ctx context.Context) (int, error) {
	recs, err := q.store.Claim(ctx, q.now().UTC(), q.lease, q.batch)
	if err != nil {
		return 0, err
	}
	var (
		mu   sync.Mutex
		sent int
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, q.workers)
	)
	for _, rec := range recs {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			ok, err := q.deliver(ctx, rec)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				sent++
			}
			if err != nil {
				errs = append(errs, err)
			}
		})
	}
	wg.Wait()
	if q.retention > 0 {
		if _, err := q.store.Prune(ctx, q.now().UTC().Add(-q.retention)); err != nil {
			errs = append(errs, err)
		}
	}
	return sent, errors.Join(errs...)
}

// deliver sends one claimed record and stores the outcome. It reports
// whether the message was sent; the error is a storage failure.
func (q *Queue) deliver(ctx context.Context, rec *Record) (bool, error) {
	// Stop before the lease runs out, so no other worker can take the message
	// while this send is still in flight.
	sendCtx, cancel := context.WithTimeout(ctx, q.lease*9/10)
	defer cancel()

	var res *notijet.Result
	msg, err := rec.Decode()
	if err != nil {
		err = notijet.Invalid(Name, "stored message is unreadable: "+err.Error())
	} else if rec.Provider != "" {
		res, err = q.notifier.SendVia(sendCtx, rec.Provider, msg)
	} else {
		res, err = q.notifier.Send(sendCtx, notijet.Channel(rec.Channel), msg)
	}
	if ctx.Err() != nil && err != nil {
		// Shutting down: leave the record claimed rather than record a failure
		// caused by the cancellation; its lease expires and a later pass sends
		// it again (the claim already counted the attempt).
		return false, nil
	}

	now := q.now().UTC()
	rec.UpdatedAt, rec.LastError, rec.ErrorCode = now, "", ""
	var partial *notijet.PartialError
	switch {
	case err == nil:
		rec.Status, rec.SentAt, rec.SentVia = StatusSent, &now, res.Provider
		rec.Receipts, _ = json.Marshal(res.Receipts)
	case errors.As(err, &partial):
		rec.Status = StatusPartial
		if partial.Result != nil {
			rec.SentVia = partial.Result.Provider
			rec.Receipts, _ = json.Marshal(partial.Result.Receipts)
		}
	case notijet.IsRetryable(err) && rec.Attempts < q.maxAttempts:
		rec.Status, rec.NextAttemptAt = StatusQueued, now.Add(q.backoff(rec.Attempts))
	default:
		rec.Status = StatusFailed
	}
	if err != nil {
		rec.LastError, rec.ErrorCode = err.Error(), notijet.ProviderCode(err)
	}

	ok, ferr := q.store.Finish(ctx, rec)
	switch {
	case ferr != nil:
		return false, ferr
	case !ok:
		q.log.WarnContext(ctx, "notijet queue: lease lost before the outcome was stored", "id", rec.ID)
		return false, nil
	}
	switch rec.Status {
	case StatusQueued:
		q.log.WarnContext(ctx, "notijet queue: delivery failed, will retry",
			"id", rec.ID, "attempt", rec.Attempts, "next", rec.NextAttemptAt, "error", err)
	case StatusFailed, StatusPartial:
		q.log.ErrorContext(ctx, "notijet queue: delivery failed permanently",
			"id", rec.ID, "status", rec.Status, "attempts", rec.Attempts, "error", err)
	}
	return rec.Status == StatusSent, nil
}

// backoff returns the jittered delay after the given failed attempt.
func (q *Queue) backoff(attempt int) time.Duration {
	d := q.backoffInitial
	for i := 1; i < attempt && d < q.backoffMax; i++ {
		d *= 2
	}
	d = min(d, q.backoffMax)
	half := d / 2
	return half + mrand.N(d-half+1)
}
