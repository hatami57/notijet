package awsutil

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	apiErr, mapped := Classify("p", errors.New("dial tcp: timeout"))
	assert.Nil(t, apiErr)
	assert.True(t, notijet.IsRetryable(mapped))

	_, mapped = Classify("p", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"})
	assert.ErrorIs(t, mapped, notijet.ErrUnauthorized)
	assert.Equal(t, "AccessDeniedException", notijet.ProviderCode(mapped))

	_, mapped = Classify("p", &smithy.GenericAPIError{Code: "TooManyRequestsException"})
	assert.ErrorIs(t, mapped, notijet.ErrRateLimited)

	_, mapped = Classify("p", &smithy.GenericAPIError{Code: "InternalFailure", Fault: smithy.FaultServer})
	assert.True(t, notijet.IsRetryable(mapped))
	assert.Equal(t, "InternalFailure", notijet.ProviderCode(mapped))

	apiErr, mapped = Classify("p", &smithy.GenericAPIError{Code: "MessageRejected", Message: "bad", Fault: smithy.FaultClient})
	assert.Nil(t, mapped)
	assert.Equal(t, "MessageRejected", apiErr.ErrorCode())
	assert.Equal(t, "bad", apiErr.ErrorMessage())
}

func TestValidTag(t *testing.T) {
	assert.True(t, ValidTag("order_id-1"))
	for _, s := range []string{"", "a b", "a.b", "علی", string(make([]byte, 257))} {
		assert.False(t, ValidTag(s), s)
	}
}
