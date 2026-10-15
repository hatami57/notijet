// Package sendgrid is a notijet email provider for Twilio SendGrid's v3 Mail
// Send API (https://www.twilio.com/docs/sendgrid/api-reference/mail-send).
//
//	p := sendgrid.New(apiKey, sendgrid.WithFrom("Acme <no-reply@acme.com>"))
//	p.Send(ctx, &notijet.Message{To: []string{"user@example.com"}, Subject: "Hi", HTML: "<p>Hi</p>"})
//
// A message with a Template is sent as a dynamic template: Template.ID is the
// template ID (d-…) and the params become dynamic_template_data. Metadata is
// sent as custom_args, which SendGrid echoes in event webhooks. Use
// WithBaseURL(EUBaseURL) for an EU-pinned account.
//
// The message goes in one personalization, so every recipient sees the same
// To and CC; SendGrid requires at least one To recipient.
package sendgrid

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/emailutil"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/mimex"
)

// Name is the provider name.
const Name = "sendgrid"

// API roots.
const (
	DefaultBaseURL = "https://api.sendgrid.com"
	EUBaseURL      = "https://api.eu.sendgrid.com"
)

// Provider sends email through SendGrid.
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

// WithBaseURL overrides the API root (EUBaseURL for EU accounts, or a test
// server).
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a SendGrid provider for apiKey.
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

type personalization struct {
	To                  []address         `json:"to"`
	CC                  []address         `json:"cc,omitempty"`
	BCC                 []address         `json:"bcc,omitempty"`
	DynamicTemplateData map[string]string `json:"dynamic_template_data,omitempty"`
}

type content struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type attachment struct {
	Content     string `json:"content"`
	Type        string `json:"type"`
	Filename    string `json:"filename"`
	Disposition string `json:"disposition"`
	ContentID   string `json:"content_id,omitempty"`
}

type request struct {
	Personalizations []personalization `json:"personalizations"`
	From             address           `json:"from"`
	ReplyTo          *address          `json:"reply_to,omitempty"`
	ReplyToList      []address         `json:"reply_to_list,omitempty"`
	Subject          string            `json:"subject,omitempty"`
	Content          []content         `json:"content,omitempty"`
	Attachments      []attachment      `json:"attachments,omitempty"`
	TemplateID       string            `json:"template_id,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	CustomArgs       map[string]string `json:"custom_args,omitempty"`
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
	if len(env.To) == 0 {
		return nil, notijet.Invalid(Name, "SendGrid needs at least one To recipient")
	}

	// SendGrid rejects an address repeated within a personalization.
	seen := map[string]bool{}
	pers := personalization{To: addresses(env.To, seen), CC: addresses(env.CC, seen), BCC: addresses(env.BCC, seen)}
	req := request{
		From:       address{Email: env.From.Address, Name: env.From.Name},
		Subject:    msg.Subject,
		Headers:    msg.Headers,
		CustomArgs: msg.Metadata,
	}
	switch len(env.ReplyTo) {
	case 0:
	case 1:
		req.ReplyTo = &address{Email: env.ReplyTo[0].Address, Name: env.ReplyTo[0].Name}
	default:
		req.ReplyToList = addresses(env.ReplyTo, map[string]bool{})
	}
	if useTemplate {
		req.TemplateID = msg.Template.ID
		pers.DynamicTemplateData = msg.Template.Map()
	} else {
		if msg.Text != "" {
			req.Content = append(req.Content, content{Type: "text/plain", Value: msg.Text})
		}
		if msg.HTML != "" {
			req.Content = append(req.Content, content{Type: "text/html", Value: msg.HTML})
		}
	}
	req.Personalizations = []personalization{pers}
	for _, a := range msg.Attachments {
		att := attachment{
			Content:     base64.StdEncoding.EncodeToString(a.Content),
			Type:        mimex.ContentType(a),
			Filename:    a.Filename,
			Disposition: "attachment",
		}
		if a.Inline() {
			att.Disposition, att.ContentID = "inline", strings.Trim(a.ContentID, "<>")
			if att.Filename == "" {
				att.Filename = att.ContentID
			}
		}
		req.Attachments = append(req.Attachments, att)
	}

	r, err := httpc.JSON(http.MethodPost, p.baseURL+"/v3/mail/send", req)
	if err != nil {
		return nil, notijet.Fault(Name, "encoding request failed", err)
	}
	resp, err := p.client.Do(ctx, r.Set("Authorization", "Bearer "+p.apiKey))
	if err != nil {
		return nil, err
	}
	switch {
	case !resp.OK():
		return nil, mapError(resp.Status, resp.Body)
	case resp.Status != http.StatusAccepted:
		// SendGrid answers 202; another 2xx is something in between (a proxy,
		// a captive portal) and does not mean the mail was accepted.
		return nil, p.client.Unexpected(resp, "")
	}
	return &notijet.Result{Provider: Name, Receipts: emailutil.Receipt(resp.Header.Get("X-Message-Id"), "accepted")}, nil
}

func addresses(list []*mail.Address, seen map[string]bool) []address {
	var out []address
	for _, a := range list {
		key := strings.ToLower(a.Address)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, address{Email: a.Address, Name: a.Name})
	}
	return out
}

type errorReply struct {
	Errors []struct {
		Message string  `json:"message"`
		Field   *string `json:"field"`
	} `json:"errors"`
}

// mapError turns a SendGrid error reply into a notijet error.
func mapError(status int, body []byte) error {
	code := strconv.Itoa(status)
	var r errorReply
	_ = json.Unmarshal(body, &r)
	var msgs, fields []string
	for _, e := range r.Errors {
		msgs = append(msgs, e.Message)
		if e.Field != nil {
			fields = append(fields, *e.Field)
		}
	}
	message := strings.Join(msgs, "; ")
	if message == "" {
		message = httpc.Snippet(body)
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "credits"):
		return notijet.Rejected(Name, code, message, notijet.ErrInsufficientCredit)
	case strings.Contains(lower, "sender identity") || slices.Contains(fields, "from.email") || slices.Contains(fields, "from"):
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case slices.Contains(fields, "template_id"):
		return notijet.Rejected(Name, code, message, notijet.ErrTemplate)
	}
	for _, f := range fields {
		if strings.HasPrefix(f, "personalizations") && strings.HasSuffix(f, "email") {
			return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
		}
	}
	return notijet.Rejected(Name, code, message, nil)
}
