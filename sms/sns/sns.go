// Package sns is a notijet SMS provider for Amazon SNS direct-to-phone
// publishing (https://docs.aws.amazon.com/sns/latest/dg/sms_sending-overview.html).
//
// It takes an SNS client from the AWS SDK for Go v2, so credentials, region
// and retries come from your AWS configuration:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	p := sns.New(awssns.NewFromConfig(cfg), sns.WithSender("Acme"))
//	p.Send(ctx, &notijet.Message{To: []string{"+447700900123"}, Text: "Your code is 1234"})
//
// Each recipient gets its own Publish call. Messages are Transactional unless
// WithSMSType says otherwise. Message.From (or WithSender) is sent as the
// sender ID, or as the origination number when it starts with "+".
package sns

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/awsutil"
	"github.com/hatami57/notijet/internal/smsutil"
	"github.com/hatami57/notijet/phone"
)

// Name is the provider name.
const Name = "sns"

// SMS types SNS accepts for AWS.SNS.SMS.SMSType.
const (
	Transactional = "Transactional"
	Promotional   = "Promotional"
)

// API is the part of the SNS client the provider uses; *sns.Client satisfies
// it.
type API interface {
	Publish(ctx context.Context, in *awssns.PublishInput, optFns ...func(*awssns.Options)) (*awssns.PublishOutput, error)
}

// Provider sends SMS through Amazon SNS.
type Provider struct {
	api     API
	sender  string
	smsType string
	country string
}

// Option configures a Provider.
type Option func(*Provider)

// WithSender sets the default sender ID, or origination number when it starts
// with "+", used when Message.From is empty.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithSMSType sets the SMS type: Transactional (default) or Promotional.
func WithSMSType(t string) Option { return func(p *Provider) { p.smsType = t } }

// WithDefaultCountry sets the calling code assumed for numbers written in
// national form with a leading 0.
func WithDefaultCountry(code string) Option { return func(p *Provider) { p.country = code } }

// New returns an SNS provider publishing through api.
func New(api API, opts ...Option) *Provider {
	p := &Provider{api: api, smsType: Transactional}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelSMS }

// Send delivers msg, one Publish call per recipient.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if _, err := notijet.CheckSMS(Name, msg, false); err != nil {
		return nil, err
	}
	if p.api == nil {
		return nil, notijet.Invalid(Name, "SNS client is nil")
	}
	to, err := smsutil.Recipients(Name, msg, func(s string) (string, error) { return phone.E164(s, p.country) })
	if err != nil {
		return nil, err
	}
	sender, _ := smsutil.Sender(Name, msg, p.sender, false)

	attrs := map[string]types.MessageAttributeValue{
		"AWS.SNS.SMS.SMSType": stringAttr(p.smsType),
	}
	switch {
	case strings.HasPrefix(sender, "+"):
		attrs["AWS.MM.SMS.OriginationNumber"] = stringAttr(sender)
	case sender != "":
		attrs["AWS.SNS.SMS.SenderID"] = stringAttr(sender)
	}

	return notijet.SendBatches(ctx, Name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		out, err := p.api.Publish(ctx, &awssns.PublishInput{
			PhoneNumber:       aws.String(batch[0]),
			Message:           aws.String(msg.Text),
			MessageAttributes: attrs,
		})
		if err != nil {
			return nil, mapError(err)
		}
		if out == nil || aws.ToString(out.MessageId) == "" {
			return nil, notijet.Fault(Name, "Publish returned no message ID", nil)
		}
		return []notijet.Receipt{{To: batch[0], MessageID: aws.ToString(out.MessageId)}}, nil
	})
}

func stringAttr(v string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
}

// mapError turns an SNS error into a notijet error.
func mapError(err error) error {
	apiErr, mapped := awsutil.Classify(Name, err)
	if mapped != nil {
		return mapped
	}
	code, message := apiErr.ErrorCode(), apiErr.ErrorMessage()
	if (code == "InvalidParameter" || code == "InvalidParameterValue") && strings.Contains(strings.ToLower(message), "phone") {
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
	}
	return notijet.Rejected(Name, code, message, nil)
}
