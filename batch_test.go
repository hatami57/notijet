package notijet_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendBatches(t *testing.T) {
	var batches [][]string
	send := func(_ context.Context, batch []string) ([]notijet.Receipt, error) {
		batches = append(batches, batch)
		if len(batches) == 3 {
			return nil, notijet.Fault("p", "down", nil)
		}
		out := make([]notijet.Receipt, len(batch))
		for i, to := range batch {
			out[i] = notijet.Receipt{To: to, MessageID: "id-" + to}
		}
		return out, nil
	}

	res, err := notijet.SendBatches(context.Background(), "p", []string{"a", "b", "c"}, 2, send)
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"a", "b"}, {"c"}}, batches)
	assert.Equal(t, "p", res.Provider)
	assert.Len(t, res.Receipts, 3)

	// The third call fails after two succeeded: a partial, non-retryable error.
	batches = batches[:1]
	_, err = notijet.SendBatches(context.Background(), "p", []string{"x", "y", "z"}, 1, send)
	var pe *notijet.PartialError
	require.ErrorAs(t, err, &pe)
	assert.Len(t, pe.Result.Receipts, 1)
	assert.False(t, notijet.IsRetryable(err))
	assert.Contains(t, err.Error(), "partially sent (1 receipts)")

	// A failure on the first call is returned as is.
	batches = batches[:2]
	_, err = notijet.SendBatches(context.Background(), "p", []string{"x"}, 0, send)
	assert.False(t, errors.As(err, &pe))
	assert.True(t, notijet.IsRetryable(err))

	ctx, cancel := context.WithCancel(context.Background())
	batches = nil
	_, err = notijet.SendBatches(ctx, "p", []string{"x", "y"}, 1, func(ctx context.Context, b []string) ([]notijet.Receipt, error) {
		cancel()
		return send(ctx, b)
	})
	require.ErrorAs(t, err, &pe)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFailoverStopsOnPartialError(t *testing.T) {
	a := memory.New(memory.WithName("a"))
	b := memory.New(memory.WithName("b"))
	c := memory.New(memory.WithName("c"))
	a.FailNext(notijet.Fault("a", "down", nil))
	b.FailNext(&notijet.PartialError{Result: &notijet.Result{}, Err: notijet.Fault("b", "down", nil)})
	_, err := notijet.Failover(a, b, c).Send(context.Background(), sms("0912"))
	var fe *notijet.FailoverError
	require.ErrorAs(t, err, &fe)
	assert.Len(t, fe.Errors, 2)
	assert.Equal(t, 0, c.Calls())
	assert.False(t, notijet.IsRetryable(err), "a partly delivered chain must not be retried whole")
}

func TestCompositeErrorsAreNilSafe(t *testing.T) {
	var partial *notijet.PartialError
	assert.Equal(t, "<nil partial error>", partial.Error())
	assert.Nil(t, partial.Unwrap())
	assert.Contains(t, (&notijet.PartialError{}).Error(), "<nil error>")

	var failover *notijet.FailoverError
	assert.Equal(t, "<nil failover error>", failover.Error())
	assert.Nil(t, failover.Unwrap())
	assert.Contains(t, (&notijet.FailoverError{Errors: []error{nil}}).Error(), "<nil error>")
}
