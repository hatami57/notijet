package sns

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/smithy-go"
	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fake struct {
	inputs []*awssns.PublishInput
	err    error
}

func (f *fake) Publish(_ context.Context, in *awssns.PublishInput, _ ...func(*awssns.Options)) (*awssns.PublishOutput, error) {
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return nil, f.err
	}
	return &awssns.PublishOutput{MessageId: aws.String("mid-" + aws.ToString(in.PhoneNumber))}, nil
}

var _ API = (*awssns.Client)(nil)

func attr(in *awssns.PublishInput, name string) string {
	v, ok := in.MessageAttributes[name]
	if !ok {
		return ""
	}
	return aws.ToString(v.StringValue)
}

func TestSend(t *testing.T) {
	f := &fake{}
	p := New(f, WithSender("Acme"), WithDefaultCountry("44"))
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"07700 900123", "+14155550100"}, Text: "hi"})
	require.NoError(t, err)
	require.Len(t, f.inputs, 2)
	in := f.inputs[0]
	assert.Equal(t, "+447700900123", aws.ToString(in.PhoneNumber))
	assert.Equal(t, "hi", aws.ToString(in.Message))
	assert.Equal(t, "Acme", attr(in, "AWS.SNS.SMS.SenderID"))
	assert.Equal(t, Transactional, attr(in, "AWS.SNS.SMS.SMSType"))
	assert.Equal(t, []string{"mid-+447700900123", "mid-+14155550100"}, res.MessageIDs())
	assert.Equal(t, "sns", res.Provider)

	f.inputs = nil
	_, err = New(f, WithSMSType(Promotional)).Send(context.Background(), &notijet.Message{To: []string{"+14155550100"}, From: "+18005550199", Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, "+18005550199", attr(f.inputs[0], "AWS.MM.SMS.OriginationNumber"))
	assert.Empty(t, attr(f.inputs[0], "AWS.SNS.SMS.SenderID"))
	assert.Equal(t, Promotional, attr(f.inputs[0], "AWS.SNS.SMS.SMSType"))
}

func TestErrors(t *testing.T) {
	cases := []struct {
		err      error
		code     string
		sentinel error
		retry    bool
	}{
		{&smithy.GenericAPIError{Code: "AuthorizationError", Message: "denied"}, "AuthorizationError", notijet.ErrUnauthorized, false},
		{&smithy.GenericAPIError{Code: "Throttling", Message: "slow"}, "Throttling", notijet.ErrRateLimited, true},
		{&smithy.GenericAPIError{Code: "InvalidParameter", Message: "Invalid parameter: PhoneNumber"}, "InvalidParameter", notijet.ErrInvalidRecipient, false},
		{&smithy.GenericAPIError{Code: "InvalidParameter", Message: "Invalid parameter: Message too long"}, "InvalidParameter", nil, false},
		{&smithy.GenericAPIError{Code: "InternalError", Fault: smithy.FaultServer}, "InternalError", nil, true},
		{&smithy.GenericAPIError{Code: "EndpointDisabled", Fault: smithy.FaultClient}, "EndpointDisabled", nil, false},
		{errors.New("dial tcp: timeout"), "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			_, err := New(&fake{err: tc.err}).Send(context.Background(), &notijet.Message{To: []string{"+14155550100"}, Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}

	_, err := New(nil).Send(context.Background(), &notijet.Message{To: []string{"+14155550100"}, Text: "x"})
	assert.Error(t, err)
	_, err = New(&fake{}).Send(context.Background(), &notijet.Message{To: []string{"07700900123"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrInvalidRecipient)
}
