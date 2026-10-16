package queue

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/hatami57/microjet/gormx"
	"gorm.io/gorm"
)

// Store persists queued messages. NewDBStore is the default implementation;
// implement Store to keep the queue elsewhere.
type Store interface {
	// Insert adds rec, joining the transaction in ctx when there is one. It
	// reports false, without error, when a record with rec.ID already exists,
	// which makes enqueueing with a fixed Message.ID idempotent.
	Insert(ctx context.Context, rec *Record) (bool, error)
	// Claim leases up to limit records that are due — queued with
	// NextAttemptAt <= now, or sending with an expired lease — marking each
	// sending until now+lease with a fresh ClaimToken and one more attempt.
	// Concurrent callers never claim the same record.
	Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*Record, error)
	// Finish stores the outcome of a claimed record: its Status and the
	// delivery fields. It reports false when the claim was lost (the lease
	// expired and another worker took the record).
	Finish(ctx context.Context, rec *Record) (bool, error)
	// Get returns the record with id, or nil when there is none.
	Get(ctx context.Context, id string) (*Record, error)
	// Requeue makes a failed or partial record due again at now, with its
	// attempts reset. It reports false when there is no such record in a
	// final failed state.
	Requeue(ctx context.Context, id string, now time.Time) (bool, error)
	// Cancel marks a queued record (waiting for its first attempt or for a
	// retry) cancelled at now, joining the transaction in ctx when there is
	// one. It reports false when there is no queued record with id: it was
	// never enqueued, already finished, or is being sent right now.
	Cancel(ctx context.Context, id string, now time.Time) (bool, error)
	// Prune deletes final records last updated before before and returns how
	// many it deleted.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// ErrDuplicate is returned by DBStore.Insert when a record with the same ID
// was inserted concurrently (the primary key caught it). On databases that
// abort a transaction on a failed statement (Postgres), the caller's
// transaction is no longer usable.
var ErrDuplicate = errors.New("queue: a message with this ID was enqueued concurrently")

// DBStore is the gormx-backed Store. It goes through gormx tables, so Insert
// and Cancel join the transaction in the context: one opened by gormx
// BaseRepository.RunTx, or a gorm transaction attached with gormx.WithTx.
type DBStore struct {
	db    *gorm.DB
	table *gormx.Table[Record]
}

// NewDBStore returns a Store on db. Call Migrate once before use (the
// microjet module does).
func NewDBStore(db *gorm.DB) *DBStore {
	return &DBStore{db: db, table: gormx.NewTable[Record](db)}
}

// Migrate creates or updates the notijet_messages table.
func (s *DBStore) Migrate() error { return s.db.AutoMigrate(&Record{}) }

// Insert implements Store.
func (s *DBStore) Insert(ctx context.Context, rec *Record) (bool, error) {
	existing, err := s.table.First(ctx, "id = ?", rec.ID)
	if err != nil {
		return false, err
	}
	if existing != nil {
		return false, nil
	}
	if err := s.table.Create(ctx, rec); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return false, ErrDuplicate
		}
		return false, err
	}
	return true, nil
}

// due selects records a worker may claim.
const due = "((status = ? AND next_attempt_at <= ?) OR (status = ? AND locked_until < ?))"

// Claim implements Store. Candidates are read first, then each is taken with
// a conditional UPDATE that only one worker can win, so claiming needs no
// row locks and behaves the same on every database.
func (s *DBStore) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*Record, error) {
	args := []any{StatusQueued, now, StatusSending, now}
	var ids []string
	if err := s.table.Order("next_attempt_at ASC").Limit(limit).Pluck(ctx, "id", &ids, due, args[0], args[1], args[2], args[3]); err != nil {
		return nil, err
	}
	var claimed []*Record
	until := now.Add(lease)
	for _, id := range ids {
		token := rand.Text()
		n, err := s.table.UpdateMap(ctx, map[string]any{
			"status":       StatusSending,
			"locked_until": until,
			"claim_token":  token,
			"attempts":     gorm.Expr("attempts + 1"),
			"updated_at":   now,
		}, "id = ? AND "+due, id, args[0], args[1], args[2], args[3])
		if err != nil {
			return claimed, err
		}
		if n == 0 {
			continue // another worker won it
		}
		rec, err := s.Get(ctx, id)
		if err != nil {
			return claimed, err
		}
		if rec != nil && rec.ClaimToken == token {
			claimed = append(claimed, rec)
		}
	}
	return claimed, nil
}

// Finish implements Store.
func (s *DBStore) Finish(ctx context.Context, rec *Record) (bool, error) {
	n, err := s.table.UpdateMap(ctx, map[string]any{
		"status":          rec.Status,
		"next_attempt_at": rec.NextAttemptAt,
		"locked_until":    nil,
		"claim_token":     "",
		"sent_via":        rec.SentVia,
		"receipts":        rec.Receipts,
		"last_error":      rec.LastError,
		"error_code":      rec.ErrorCode,
		"sent_at":         rec.SentAt,
		"updated_at":      rec.UpdatedAt,
	}, "id = ? AND claim_token = ? AND status = ?", rec.ID, rec.ClaimToken, StatusSending)
	return n == 1, err
}

// Get implements Store.
func (s *DBStore) Get(ctx context.Context, id string) (*Record, error) {
	return s.table.First(ctx, "id = ?", id)
}

// Requeue implements Store.
func (s *DBStore) Requeue(ctx context.Context, id string, now time.Time) (bool, error) {
	n, err := s.table.UpdateMap(ctx, map[string]any{
		"status":          StatusQueued,
		"attempts":        0,
		"next_attempt_at": now,
		"updated_at":      now,
	}, "id = ? AND status IN ?", id, []Status{StatusFailed, StatusPartial})
	return n == 1, err
}

// Cancel implements Store.
func (s *DBStore) Cancel(ctx context.Context, id string, now time.Time) (bool, error) {
	n, err := s.table.UpdateMap(ctx, map[string]any{
		"status":     StatusCancelled,
		"updated_at": now,
	}, "id = ? AND status = ?", id, StatusQueued)
	return n == 1, err
}

// Prune implements Store.
func (s *DBStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	return s.table.Delete(ctx, "status IN ? AND updated_at < ?", finalStatuses, before)
}
