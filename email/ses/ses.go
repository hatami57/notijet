// Package ses is a notijet email provider for Amazon SES, API v2
// (https://docs.aws.amazon.com/ses/latest/APIReference-V2/API_SendEmail.html).
//
// It takes an SES v2 client from the AWS SDK for Go v2, so credentials,
// region and retries come from your AWS configuration:
//
//	cfg, _ := config.LoadDefaultConfig(ctx)
//	p := ses.New(sesv2.NewFromConfig(cfg), ses.WithFrom("Acme <no-reply@acme.com>"))
//	p.Send(ctx, &notijet.Message{To: []string{"user@example.com"}, Subject: "Hi", Text: "Hi"})
//
// Messages are sent as SES "simple" content, attachments and custom headers
// included. A message with a Template is sent with the stored template named
// by Template.ID, its params as the template data. Metadata becomes message
// tags (ASCII letters, digits, '_' and '-'), which SES adds to its event
// publishing records.
package ses

import (
	"context"
	"encoding/json"
	"maps"
	"net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/awsutil"
	"github.com/hatami57/notijet/internal/emailutil"
	"github.com/hatami57/notijet/internal/mimex"
)

// Name is the provider name.
const Name = "ses"

// API is the part of the SES v2 client the provider uses; *sesv2.Client
// satisfies it.
type API interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// Provider sends email through Amazon SES.
type Provider struct {
	api       API
	from      string
	configSet string
}

// Option configures a Provider.
type Option func(*Provider)

// WithFrom sets the From address used when Message.From is empty.
func WithFrom(from string) Option { return func(p *Provider) { p.from = from } }

// WithConfigurationSet sets the configuration set every message is sent with
// (event publishing, dedicated IP pools ...).
func WithConfigurationSet(name string) Option { return func(p *Provider) { p.configSet = name } }

// New returns an SES provider sending through api.
func New(api API, opts ...Option) *Provider {
	p := &Provider{api: api}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelEmail }

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	env, useTemplate, err := emailutil.Prepare(Name, msg, p.from, true)
	if err != nil {
		return nil, err
	}
	if p.api == nil {
		return nil, notijet.Invalid(Name, "SES client is nil")
	}

	in := &sesv2.SendEmailInput{
		// SES wants non-ASCII display names as MIME encoded-words, which is
		// what mail.Address.String produces.
		FromEmailAddress: aws.String(format(env.From)),
		Destination: &types.Destination{
			ToAddresses:  formatted(env.To),
			CcAddresses:  formatted(env.CC),
			BccAddresses: formatted(env.BCC),
		},
		ReplyToAddresses: formatted(env.ReplyTo),
		Content:          &types.EmailContent{},
	}
	if p.configSet != "" {
		in.ConfigurationSetName = aws.String(p.configSet)
	}
	for _, k := range slices.Sorted(maps.Keys(msg.Metadata)) {
		v := msg.Metadata[k]
		if !emailutil.ValidTag(k) || !emailutil.ValidTag(v) {
			return nil, notijet.Invalid(Name, "Metadata "+strconv.Quote(k)+": SES tags allow only ASCII letters, digits, _ and - (up to 256)")
		}
		in.EmailTags = append(in.EmailTags, types.MessageTag{Name: aws.String(k), Value: aws.String(v)})
	}
	headers := headerList(msg.Headers)
	attachments := attachmentList(msg.Attachments)

	if useTemplate {
		data, _ := json.Marshal(msg.Template.Map())
		in.Content.Template = &types.Template{
			TemplateName: aws.String(msg.Template.ID),
			TemplateData: aws.String(string(data)),
			Headers:      headers,
			Attachments:  attachments,
		}
	} else {
		body := &types.Body{}
		if msg.Text != "" {
			body.Text = utf8(msg.Text)
		}
		if msg.HTML != "" {
			body.Html = utf8(msg.HTML)
		}
		in.Content.Simple = &types.Message{Subject: utf8(msg.Subject), Body: body, Headers: headers, Attachments: attachments}
	}

	out, err := p.api.SendEmail(ctx, in)
	if err != nil {
		return nil, mapError(err, useTemplate)
	}
	if out == nil || aws.ToString(out.MessageId) == "" {
		return nil, notijet.Fault(Name, "SendEmail returned no message ID", nil)
	}
	return &notijet.Result{Provider: Name, Receipts: emailutil.Receipt(aws.ToString(out.MessageId), "accepted")}, nil
}

func formatted(list []*mail.Address) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = format(a)
	}
	return out
}

// format returns a bare address when a has no name, otherwise the name as a
// MIME encoded-word when it is not ASCII, as SES requires.
func format(a *mail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.String()
}

func utf8(s string) *types.Content {
	return &types.Content{Data: aws.String(s), Charset: aws.String("UTF-8")}
}

func headerList(h map[string]string) []types.MessageHeader {
	var out []types.MessageHeader
	for _, k := range slices.Sorted(maps.Keys(h)) {
		out = append(out, types.MessageHeader{Name: aws.String(k), Value: aws.String(h[k])})
	}
	return out
}

func attachmentList(list []notijet.Attachment) []types.Attachment {
	var out []types.Attachment
	for _, a := range list {
		att := types.Attachment{
			FileName:           aws.String(a.Filename),
			RawContent:         a.Content,
			ContentType:        aws.String(mimex.ContentType(a)),
			ContentDisposition: types.AttachmentContentDispositionAttachment,
		}
		if a.Inline() {
			cid := strings.Trim(a.ContentID, "<>")
			att.ContentDisposition = types.AttachmentContentDispositionInline
			att.ContentId = aws.String(cid)
			if a.Filename == "" {
				att.FileName = aws.String(cid)
			}
		}
		out = append(out, att)
	}
	return out
}

// mapError turns an SES error into a notijet error.
func mapError(err error, template bool) error {
	apiErr, mapped := awsutil.Classify(Name, err)
	if mapped != nil {
		return mapped
	}
	code, message := apiErr.ErrorCode(), apiErr.ErrorMessage()
	lower := strings.ToLower(message)
	switch {
	case code == "MailFromDomainNotVerifiedException":
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
	case code == "AccountSuspendedException" || code == "SendingPausedException":
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case template && (code == "NotFoundException" || strings.Contains(lower, "template")):
		return notijet.Rejected(Name, code, message, notijet.ErrTemplate)
	}
	return notijet.Rejected(Name, code, message, nil)
}
