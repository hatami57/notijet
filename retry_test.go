package notijet

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryDelay(t *testing.T) {
	r := Retry(nil, RetryBackoff(time.Second, 5*time.Second)).(*retry)
	bounds := []struct{ lo, hi time.Duration }{
		{500 * time.Millisecond, time.Second},
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{2500 * time.Millisecond, 5 * time.Second}, // capped
		{2500 * time.Millisecond, 5 * time.Second},
	}
	for i, b := range bounds {
		for range 100 {
			d := r.delay(i + 1)
			assert.GreaterOrEqual(t, d, b.lo, "attempt %d", i+1)
			assert.LessOrEqual(t, d, b.hi, "attempt %d", i+1)
		}
	}
	assert.Equal(t, time.Duration(0), Retry(nil, RetryBackoff(0, 0)).(*retry).delay(3))
	assert.Equal(t, 1, Retry(nil, RetryAttempts(-2)).(*retry).attempts)
}

func TestRetryDelayDoesNotOverflow(t *testing.T) {
	maxDelay := time.Duration(1<<63 - 1)
	r := Retry(nil, RetryBackoff(time.Duration(1<<62), maxDelay)).(*retry)
	d := r.delay(2)
	assert.Greater(t, d, time.Duration(0))
	assert.LessOrEqual(t, d, maxDelay)
}
