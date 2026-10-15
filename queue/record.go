// Package queue persists notifications and delivers them in the background,
// so sending survives provider outages and process restarts and can join
// the database transaction that makes the change being announced.
//
//	q := queue.New(queue.NewDBStore(db), notifier)
//	err := repo.RunTx(ctx, func(ctx context.Context) error {
//	    if err := orders.Create(ctx, &order); err != nil {
//	        return err
//	    }
//	    _, err := q.Enqueue(ctx, notijet.ChannelSMS, msg) // commits with the order
//	    return err
//	})
//	go q.Run(ctx) // or let the microjet module run the worker
//
// Delivery goes through a notijet.Notifier, so provider choice, failover,
// hooks and logging are the Notifier's. Retryable failures (see
// notijet.IsRetryable) are retried with exponential backoff up to
// MaxAttempts; permanent failures, and messages that ran out of attempts, are
// marked failed and kept for inspection (Retry requeues one). A message that
// was partly delivered (notijet.PartialError) is never resent.
//
// Workers claim messages with a lease, one conditional UPDATE per message, so
// any number of workers — in one process or many replicas — share a queue on
// any database, SQLite included; a worker that dies mid-send releases its
// messages when the lease expires. Delivery is therefore at-least-once:
// a crash after the provider accepted a message but before the outcome was
// recorded sends it again. Set Message.ID, which providers forward where they
// can, to let the platform or the recipient spot a duplicate.
package queue

import (
	"encoding/json"
	"time"

	"github.com/hatami57/notijet"
)

// Status is the lifecycle state of a queued message.
type Status string

const (
	// StatusQueued waits for its NextAttemptAt.
	StatusQueued Status = "queued"
	// StatusSending is claimed by a worker until LockedUntil.
	StatusSending Status = "sending"
	// StatusSent was accepted by a provider.
	StatusSent Status = "sent"
	// StatusPartial reached some recipients before failing; it is not retried.
	StatusPartial Status = "partial"
	// StatusFailed failed permanently or ran out of attempts.
	StatusFailed Status = "failed"
)

// Record is a queued message and its delivery state. The GORM tags serve the
// default database store and are inert for other Store implementations.
type Record struct {
	ID       string `gorm:"primaryKey;size:191"`
	Channel  string `gorm:"size:32;not null;index"`
	Provider string `gorm:"size:191"` // registered provider name; empty means the channel default
	// Message is the JSON-encoded notijet.Message.
	Message []byte `gorm:"not null"`

	Status        Status    `gorm:"size:16;not null;index:idx_notijet_messages_due,priority:1"`
	Attempts      int       `gorm:"not null;default:0"`
	NextAttemptAt time.Time `gorm:"not null;index:idx_notijet_messages_due,priority:2"`
	LockedUntil   *time.Time
	// ClaimToken identifies the worker's current claim, so a worker whose
	// lease expired cannot overwrite the outcome of the one that took over.
	ClaimToken string `gorm:"size:64"`

	SentVia   string `gorm:"size:191"` // Result.Provider of the delivery
	Receipts  []byte // JSON-encoded []notijet.Receipt
	LastError string
	ErrorCode string `gorm:"size:191"`

	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null;index"`
	SentAt    *time.Time
}

// TableName is the table the default store uses.
func (Record) TableName() string { return "notijet_messages" }

// Decode returns the stored message.
func (r *Record) Decode() (*notijet.Message, error) {
	var m notijet.Message
	if err := json.Unmarshal(r.Message, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeReceipts returns the stored receipts, nil when there are none.
func (r *Record) DecodeReceipts() ([]notijet.Receipt, error) {
	if len(r.Receipts) == 0 {
		return nil, nil
	}
	var out []notijet.Receipt
	err := json.Unmarshal(r.Receipts, &out)
	return out, err
}

// Done reports whether the record reached a final state.
func (r *Record) Done() bool {
	return r.Status == StatusSent || r.Status == StatusPartial || r.Status == StatusFailed
}
