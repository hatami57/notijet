// Package brevo is a notijet email provider for Brevo, formerly Sendinblue
// (https://developers.brevo.com/reference/sendtransacemail).
//
//	p := brevo.New(apiKey, brevo.WithFrom("Acme <no-reply@acme.com>"))
//	p.Send(ctx, &notijet.Message{To: []string{"user@example.com"}, Subject: "Hi", HTML: "<p>Hi</p>"})
//
// A message with a Template is sent with the template whose numeric ID is
// Template.ID, its params as the template params. Metadata is sent in the
// X-Mailin-custom header, which Brevo returns in webhook events, so its keys
// and values may not contain ":" or "|". Brevo takes a single Reply-To
// address and does not support inline (cid:) attachments.
package brevo

import (
	"context"
	"encoding/base64"
	"maps"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/emailutil"
	"github.com/hatami57/notijet/internal/httpc"
)

// Name is the provider name.
const Name = "brevo"

// DefaultBaseURL is Brevo's API root.
const DefaultBaseURL = "https://api.brevo.com/v3"

// customHeader carries Metadata to webhook events.
const customHeader = "X-Mailin-custom"

// Provider sends email through Brevo.
type Provider struct {
	apiKey  string
	from    string
	baseURL string
	client  *httpc.Client
	hc      *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithFrom sets the From address used when Message.From is empty.
func WithFrom(from string) Option { return func(p *Provider) { p.from = from } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Brevo provider for apiKey.
func New(apiKey string, opts ...Option) *Provider {
	p := &Provider{apiKey: strings.TrimSpace(apiKey), baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelEmail }

type address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type attachment struct {
	Content string `json:"content"`
	Name    string `json:"name"`
}

type request struct {
	Sender      address           `json:"sender"`
	To          []address         `json:"to"`
	CC          []address         `json:"cc,omitempty"`
	BCC         []address         `json:"bcc,omitempty"`
	ReplyTo     *address          `json:"replyTo,omitempty"`
	Subject     string            `json:"subject,omitempty"`
	HTMLContent string            `json:"htmlContent,omitempty"`
	TextContent string            `json:"textContent,omitempty"`
	Attachment  []attachment      `json:"attachment,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	TemplateID  int64             `json:"templateId,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
}

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	env, useTemplate, err := emailutil.Prepare(Name, msg, p.from, true)
	if err != nil {
		return nil, err
	}
	if p.apiKey == "" {
		return nil, notijet.Invalid(Name, "API key is empty")
	}
	switch {
	case len(env.To) == 0:
		return nil, notijet.Invalid(Name, "Brevo needs at least one To recipient")
	case len(env.ReplyTo) > 1:
		return nil, notijet.Unsupported(Name, "more than one Reply-To address")
	}

	req := request{
		Sender:  address{Email: env.From.Address, Name: env.From.Name},
		To:      addresses(env.To),
		CC:      addresses(env.CC),
		BCC:     addresses(env.BCC),
		Subject: msg.Subject,
	}
	if len(env.ReplyTo) == 1 {
		req.ReplyTo = &address{Email: env.ReplyTo[0].Address, Name: env.ReplyTo[0].Name}
	}
	if useTemplate {
		id, err := strconv.ParseInt(strings.TrimSpace(msg.Template.ID), 10, 64)
		if err != nil {
			return nil, notijet.InvalidTemplate(Name, "Template.ID must be the numeric Brevo template ID")
		}
		req.TemplateID, req.Params = id, msg.Template.Map()
	} else {
		req.HTMLContent, req.TextContent = msg.HTML, msg.Text
	}
	for _, a := range msg.Attachments {
		if a.Inline() {
			return nil, notijet.Unsupported(Name, "inline attachments")
		}
		req.Attachment = append(req.Attachment, attachment{Content: base64.StdEncoding.EncodeToString(a.Content), Name: a.Filename})
	}
	if req.Headers, err = headers(msg); err != nil {
		return nil, err
	}

	r, err := httpc.JSON(http.MethodPost, p.baseURL+"/smtp/email", req)
	if err != nil {
		return nil, notijet.Fault(Name, "encoding request failed", err)
	}
	resp, err := p.client.Do(ctx, r.Set("api-key", p.apiKey))
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if p.client.Decode(resp, &e) != nil || e.Code == "" {
			e.Code, e.Message = strconv.Itoa(resp.Status), resp.Snippet()
		}
		return nil, mapError(resp.Status, e.Code, e.Message, useTemplate)
	}
	var out struct {
		MessageID string `json:"messageId"`
	}
	if err := p.client.Decode(resp, &out); err != nil {
		return nil, err
	}
	if out.MessageID == "" {
		return nil, p.client.Unexpected(resp, "")
	}
	return &notijet.Result{Provider: Name, Receipts: emailutil.Receipt(out.MessageID, "accepted")}, nil
}

func addresses(list []*mail.Address) []address {
	var out []address
	for _, a := range list {
		out = append(out, address{Email: a.Address, Name: a.Name})
	}
	return out
}

// headers returns the custom headers with Metadata encoded into
// X-Mailin-custom as "key:value|key:value".
func headers(msg *notijet.Message) (map[string]string, error) {
	if len(msg.Metadata) == 0 {
		return msg.Headers, nil
	}
	for k := range msg.Headers {
		if strings.EqualFold(k, customHeader) {
			return nil, notijet.Invalid(Name, "set Metadata or the "+customHeader+" header, not both")
		}
	}
	var pairs []string
	for _, k := range slices.Sorted(maps.Keys(msg.Metadata)) {
		v := msg.Metadata[k]
		if k == "" || strings.ContainsAny(k+v, ":|\r\n") {
			return nil, notijet.Invalid(Name, "Metadata "+strconv.Quote(k)+": keys and values may not contain ':' or '|'")
		}
		pairs = append(pairs, k+":"+v)
	}
	out := maps.Clone(msg.Headers)
	if out == nil {
		out = map[string]string{}
	}
	out[customHeader] = strings.Join(pairs, "|")
	return out, nil
}

// mapError turns a Brevo error reply into a notijet error.
func mapError(status int, code, message string, template bool) error {
	lower := strings.ToLower(message)
	switch code {
	case "unauthorized", "permission_denied", "account_under_validation":
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case "not_enough_credits":
		return notijet.Rejected(Name, code, message, notijet.ErrInsufficientCredit)
	case "document_not_found":
		if template {
			return notijet.Rejected(Name, code, message, notijet.ErrTemplate)
		}
	case "invalid_parameter", "missing_parameter":
		switch {
		case strings.Contains(lower, "sender"):
			return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
		case strings.Contains(lower, "template"):
			return notijet.Rejected(Name, code, message, notijet.ErrTemplate)
		case strings.Contains(lower, "email is not valid") || strings.Contains(lower, "recipient"):
			return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	}
	return notijet.Rejected(Name, code, message, nil)
}
