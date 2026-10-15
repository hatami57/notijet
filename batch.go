package notijet

import (
	"context"
	"strconv"
)

// PartialError is returned by a provider that sends a message in several
// requests — one per recipient, or one per batch of recipients — when a request
// fails after earlier ones succeeded. Result holds the receipts of the
// recipients already sent to; Err is the failure, and Unwrap returns it, so its
// category and sentinel still show through errorx and errors.Is.
//
// A PartialError is never retryable (see IsRetryable) and stops a Failover:
// sending the whole message again would duplicate what was delivered. Resend to
// the recipients missing from Result instead.
type PartialError struct {
	Result *Result
	Err    error
}

func (e *PartialError) Error() string {
	sent := 0
	if e.Result != nil {
		sent = len(e.Result.Receipts)
	}
	return "partially sent (" + strconv.Itoa(sent) + " receipts) before failing: " + e.Err.Error()
}

func (e *PartialError) Unwrap() error { return e.Err }

// SendBatches is the loop behind providers whose API takes a limited number of
// recipients per request. It calls send with consecutive groups of at most size
// recipients (size <= 0 sends them all in one call), collects the receipts into
// one Result named provider, and turns a failure after the first group into a
// *PartialError. It stops before the next group once ctx is done.
func SendBatches(ctx context.Context, provider string, recipients []string, size int,
	send func(ctx context.Context, batch []string) ([]Receipt, error)) (*Result, error) {
	if size <= 0 {
		size = len(recipients)
	}
	res := &Result{Provider: provider}
	for start := 0; start < len(recipients); start += size {
		if start > 0 && ctx.Err() != nil {
			return nil, &PartialError{Result: res, Err: Fault(provider, "context done", ctx.Err())}
		}
		receipts, err := send(ctx, recipients[start:min(start+size, len(recipients))])
		if err != nil {
			if start == 0 {
				return nil, err
			}
			return nil, &PartialError{Result: res, Err: err}
		}
		res.Receipts = append(res.Receipts, receipts...)
	}
	return res, nil
}
