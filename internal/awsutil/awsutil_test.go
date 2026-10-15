package awsutil

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	mapped, _, _, done := Classify("p", errors.New("dial tcp: timeout"))
	assert.True(t, done)
	assert.True(t, notijet.IsRetryable(mapped))

	mapped, code, _, done := Classify("p", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"})
	assert.True(t, done)
	assert.Equal(t, "AccessDeniedException", code)
	assert.ErrorIs(t, mapped, notijet.ErrUnauthorized)

	mapped, _, _, done = Classify("p", &smithy.GenericAPIError{Code: "TooManyRequestsException"})
	assert.True(t, done)
	assert.ErrorIs(t, mapped, notijet.ErrRateLimited)

	mapped, _, _, done = Classify("p", &smithy.GenericAPIError{Code: "InternalFailure", Fault: smithy.FaultServer})
	assert.True(t, done)
	assert.True(t, notijet.IsRetryable(mapped))
	assert.Equal(t, "InternalFailure", notijet.ProviderCode(mapped))

	mapped, code, msg, done := Classify("p", &smithy.GenericAPIError{Code: "MessageRejected", Message: "bad", Fault: smithy.FaultClient})
	assert.False(t, done)
	assert.Nil(t, mapped)
	assert.Equal(t, "MessageRejected", code)
	assert.Equal(t, "bad", msg)
}

func TestValidTag(t *testing.T) {
	assert.True(t, ValidTag("order_id-1"))
	for _, s := range []string{"", "a b", "a.b", "علی", string(make([]byte, 257))} {
		assert.False(t, ValidTag(s), s)
	}
}
