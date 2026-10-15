package ses

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	"github.com/hatami57/notijet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fake struct {
	in  *sesv2.SendEmailInput
	out *sesv2.SendEmailOutput
	err error
}

func (f *fake) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	if f.out != nil {
		return f.out, nil
	}
	return &sesv2.SendEmailOutput{MessageId: aws.String("0100018f-ses")}, nil
}

var _ API = (*sesv2.Client)(nil)

func TestSimple(t *testing.T) {
	f := &fake{}
	p := New(f, WithFrom("Acme <no-reply@acme.test>"), WithConfigurationSet("events"))
	res, err := p.Send(context.Background(), &notijet.Message{
		To:          []string{"علی <a@x.io>"},
		CC:          []string{"c@x.io"},
		BCC:         []string{"b@x.io"},
		ReplyTo:     []string{"r@x.io"},
		Subject:     "سلام",
		Text:        "plain",
		HTML:        "<p>html</p>",
		Headers:     map[string]string{"X-B": "2", "X-A": "1"},
		Metadata:    map[string]string{"order": "42"},
		Attachments: []notijet.Attachment{{Filename: "a.pdf", Content: []byte("PDF")}, {ContentID: "<logo>", ContentType: "image/png", Content: []byte("PNG")}},
	})
	require.NoError(t, err)
	assert.Equal(t, "0100018f-ses", res.MessageID())
	assert.Equal(t, "ses", res.Provider)

	in := f.in
	assert.Equal(t, `"Acme" <no-reply@acme.test>`, aws.ToString(in.FromEmailAddress))
	assert.Equal(t, []string{"=?utf-8?q?=D8=B9=D9=84=DB=8C?= <a@x.io>"}, in.Destination.ToAddresses, "non-ASCII names are encoded")
	assert.Equal(t, []string{"c@x.io"}, in.Destination.CcAddresses)
	assert.Equal(t, []string{"b@x.io"}, in.Destination.BccAddresses)
	assert.Equal(t, []string{"r@x.io"}, in.ReplyToAddresses)
	assert.Equal(t, "events", aws.ToString(in.ConfigurationSetName))
	assert.Equal(t, []types.MessageTag{{Name: aws.String("order"), Value: aws.String("42")}}, in.EmailTags)

	m := in.Content.Simple
	require.NotNil(t, m)
	assert.Nil(t, in.Content.Template)
	assert.Equal(t, "سلام", aws.ToString(m.Subject.Data))
	assert.Equal(t, "UTF-8", aws.ToString(m.Subject.Charset))
	assert.Equal(t, "plain", aws.ToString(m.Body.Text.Data))
	assert.Equal(t, "<p>html</p>", aws.ToString(m.Body.Html.Data))
	assert.Equal(t, []types.MessageHeader{
		{Name: aws.String("X-A"), Value: aws.String("1")},
		{Name: aws.String("X-B"), Value: aws.String("2")},
	}, m.Headers)
	require.Len(t, m.Attachments, 2)
	assert.Equal(t, types.Attachment{
		FileName: aws.String("a.pdf"), RawContent: []byte("PDF"), ContentType: aws.String("application/pdf"),
		ContentDisposition: types.AttachmentContentDispositionAttachment,
	}, m.Attachments[0])
	assert.Equal(t, types.Attachment{
		FileName: aws.String("logo"), RawContent: []byte("PNG"), ContentType: aws.String("image/png"),
		ContentDisposition: types.AttachmentContentDispositionInline, ContentId: aws.String("logo"),
	}, m.Attachments[1])
}

func TestTextOnlyAndTemplate(t *testing.T) {
	f := &fake{}
	p := New(f)
	_, err := p.Send(context.Background(), &notijet.Message{From: "a@acme.test", To: []string{"b@x.io"}, Subject: "s", Text: "t"})
	require.NoError(t, err)
	assert.Nil(t, f.in.Content.Simple.Body.Html)
	assert.Nil(t, f.in.ConfigurationSetName)
	assert.Nil(t, f.in.Destination.CcAddresses)

	_, err = p.Send(context.Background(), &notijet.Message{
		From: "a@acme.test", To: []string{"b@x.io"}, Template: notijet.NewTemplate("Welcome", "name", "Ali"),
	})
	require.NoError(t, err)
	tpl := f.in.Content.Template
	require.NotNil(t, tpl)
	assert.Nil(t, f.in.Content.Simple)
	assert.Equal(t, "Welcome", aws.ToString(tpl.TemplateName))
	assert.JSONEq(t, `{"name":"Ali"}`, aws.ToString(tpl.TemplateData))
}

func TestErrors(t *testing.T) {
	cases := []struct {
		err      error
		template bool
		code     string
		sentinel error
		retry    bool
	}{
		{&smithy.GenericAPIError{Code: "MessageRejected", Message: "Email address is not verified."}, false, "MessageRejected", nil, false},
		{&smithy.GenericAPIError{Code: "MailFromDomainNotVerifiedException"}, false, "MailFromDomainNotVerifiedException", notijet.ErrInvalidSender, false},
		{&smithy.GenericAPIError{Code: "AccountSuspendedException"}, false, "AccountSuspendedException", notijet.ErrUnauthorized, false},
		{&smithy.GenericAPIError{Code: "SendingPausedException"}, false, "SendingPausedException", notijet.ErrUnauthorized, false},
		{&smithy.GenericAPIError{Code: "NotFoundException", Message: "Template Welcome does not exist"}, true, "NotFoundException", notijet.ErrTemplate, false},
		{&smithy.GenericAPIError{Code: "NotFoundException", Message: "Configuration set does not exist"}, false, "NotFoundException", nil, false},
		{&smithy.GenericAPIError{Code: "TooManyRequestsException"}, false, "TooManyRequestsException", notijet.ErrRateLimited, true},
		{&smithy.GenericAPIError{Code: "AccessDeniedException"}, false, "AccessDeniedException", notijet.ErrUnauthorized, false},
		{&smithy.GenericAPIError{Code: "InternalFailure", Fault: smithy.FaultServer}, false, "InternalFailure", nil, true},
		{errors.New("connection reset"), false, "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			msg := &notijet.Message{From: "a@acme.test", To: []string{"b@x.io"}, Subject: "s", Text: "t"}
			if tc.template {
				msg.Template = notijet.NewTemplate("Welcome")
			}
			_, err := New(&fake{err: tc.err}).Send(context.Background(), msg)
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.Equal(t, tc.retry, notijet.IsRetryable(err))
		})
	}
}

func TestValidation(t *testing.T) {
	f := &fake{}
	p := New(f)
	assert.Equal(t, "ses", p.Name())
	assert.Equal(t, notijet.ChannelEmail, p.Channel())
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"b@x.io"}, Subject: "s", Text: "t"})
	assert.ErrorContains(t, err, "From")
	_, err = p.Send(context.Background(), &notijet.Message{From: "a@acme.test", To: []string{"b@x.io"}, Subject: "s", Text: "t", Metadata: map[string]string{"a b": "c"}})
	assert.Error(t, err)
	assert.Nil(t, f.in, "nothing is sent for an invalid message")
	_, err = New(nil).Send(context.Background(), &notijet.Message{From: "a@acme.test", To: []string{"b@x.io"}, Subject: "s", Text: "t"})
	assert.Error(t, err)

	_, err = New(&fake{out: &sesv2.SendEmailOutput{}}).Send(context.Background(), &notijet.Message{From: "a@acme.test", To: []string{"b@x.io"}, Subject: "s", Text: "t"})
	assert.True(t, notijet.IsRetryable(err), "no message ID is unexpected")
}
