package queue

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hatami57/microjet/gormx"
	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// faultyStore overrides some DBStore methods to simulate storage outcomes.
type faultyStore struct {
	*DBStore
	finish func(*Record) (bool, error)
	prune  error
}

func (s *faultyStore) Finish(ctx context.Context, rec *Record) (bool, error) {
	if s.finish != nil {
		return s.finish(rec)
	}
	return s.DBStore.Finish(ctx, rec)
}

func (s *faultyStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	if s.prune != nil {
		return 0, s.prune
	}
	return s.DBStore.Prune(ctx, before)
}

func TestStorageOutcomes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	boom := errors.New("disk full")

	lost := &faultyStore{DBStore: f.store, finish: func(*Record) (bool, error) { return false, nil }}
	q := New(lost, f.n, WithLogger(slog.New(slog.DiscardHandler)))
	_, err := q.Enqueue(ctx, notijet.ChannelSMS, sms("1"))
	require.NoError(t, err)
	n, err := q.Process(ctx)
	require.NoError(t, err, "a lost lease is logged, not an error")
	assert.Zero(t, n, "and not counted as delivered by this worker")

	failing := &faultyStore{DBStore: f.store, finish: func(*Record) (bool, error) { return false, boom }, prune: boom}
	q = New(failing, f.n, WithLogger(slog.New(slog.DiscardHandler)), Retention(time.Hour), WithClock(func() time.Time { return t0.Add(time.Hour) }))
	_, err = q.Enqueue(ctx, notijet.ChannelSMS, sms("2"))
	require.NoError(t, err)
	_, err = q.Process(ctx)
	assert.ErrorIs(t, err, boom)
}

func TestRunTicks(t *testing.T) {
	f := setup(t, Interval(20*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Inserted behind the queue's back, so no wake-up: only the ticker finds it.
	_, err := f.store.Insert(ctx, &Record{ID: "x", Channel: "sms", Message: []byte(`{"To":["1"],"Text":"hi"}`),
		Status: StatusQueued, NextAttemptAt: t0, CreatedAt: t0, UpdatedAt: t0})
	require.NoError(t, err)
	go f.q.Run(ctx)
	require.Eventually(t, func() bool { return f.sms.Len() == 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestConcurrentDuplicateInsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dup.db") + "?_pragma=busy_timeout(10000)"
	a, b := NewDBStore(openDB(t, path)), NewDBStore(openDB(t, path))
	require.NoError(t, a.Migrate())
	rec := func() *Record {
		return &Record{ID: "same", Channel: "sms", Message: []byte("{}"), Status: StatusQueued, NextAttemptAt: t0, CreatedAt: t0, UpdatedAt: t0}
	}
	inserted := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error)
	go func() {
		repo := gormx.NewBaseRepository(a.db)
		done <- repo.RunTx(context.Background(), func(ctx context.Context) error {
			if _, err := a.Insert(ctx, rec()); err != nil {
				return err
			}
			close(inserted)
			<-release
			return nil
		})
	}()
	<-inserted
	// b cannot see the uncommitted row, so it tries to insert and waits for
	// a's lock; once a commits, the primary key rejects b's row.
	errc := make(chan error)
	go func() {
		_, err := b.Insert(context.Background(), rec())
		errc <- err
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)
	require.NoError(t, <-done)
	assert.ErrorIs(t, <-errc, ErrDuplicate)
}

func TestRecordHelpers(t *testing.T) {
	r := &Record{}
	receipts, err := r.DecodeReceipts()
	assert.NoError(t, err)
	assert.Nil(t, receipts)
	assert.Equal(t, "notijet_messages", r.TableName())
}
