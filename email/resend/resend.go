// Package resend is a notijet email provider for Resend
// (https://resend.com/docs/api-reference/emails/send-email).
//
//	p := resend.New(apiKey, resend.WithFrom("Acme <no-reply@acme.com>"))
//	p.Send(ctx, &notijet.Message{To: []string{"user@example.com"}, Subject: "Hi", HTML: "<p>Hi</p>"})
//
// A message with a Template is sent with the published template whose ID or
// alias is Template.ID, its params as the template variables. Metadata is
// sent as tags, whose names and values Resend limits to ASCII letters,
// digits, "_" and "-". Message.ID, when set, is sent as the Idempotency-Key,
// so a retried send within 24 hours is not delivered twice. Resend accepts at
// most 50 To recipients.
package resend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/emailutil"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/mimex"
)

// Name is the provider name.
const Name = "resend"

// DefaultBaseURL is Resend's API root.
const DefaultBaseURL = "https://api.resend.com"

// Limits Resend documents.
const (
	MaxTo                = 50
	maxIdempotencyKeyLen = 256
)

// Provider sends email through Resend.
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

// New returns a Resend provider for apiKey.
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

type attachment struct {
	Filename    string `json:"filename,omitempty"`
	Content     string `json:"content"`
	ContentType string `json:"content_type,omitempty"`
	ContentID   string `json:"content_id,omitempty"`
}

type tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type template struct {
	ID        string            `json:"id"`
	Variables map[string]string `json:"variables,omitempty"`
}

type request struct {
	From        string            `json:"from"`
	To          []string          `json:"to"`
	CC          []string          `json:"cc,omitempty"`
	BCC         []string          `json:"bcc,omitempty"`
	ReplyTo     []string          `json:"reply_to,omitempty"`
	Subject     string            `json:"subject,omitempty"`
	HTML        string            `json:"html,omitempty"`
	Text        string            `json:"text,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Attachments []attachment      `json:"attachments,omitempty"`
	Tags        []tag             `json:"tags,omitempty"`
	Template    *template         `json:"template,omitempty"`
}

type errorReply struct {
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
	Name       string `json:"name"`
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
		return nil, notijet.Invalid(Name, "Resend needs at least one To recipient")
	case len(env.To) > MaxTo:
		return nil, notijet.Invalid(Name, "Resend accepts at most 50 To recipients")
	case len(msg.ID) > maxIdempotencyKeyLen:
		return nil, notijet.Invalid(Name, "Message.ID is longer than the 256-character idempotency key limit")
	}

	req := request{
		From:    emailutil.Display(env.From),
		To:      emailutil.DisplayList(env.To),
		CC:      emailutil.DisplayList(env.CC),
		BCC:     emailutil.DisplayList(env.BCC),
		ReplyTo: emailutil.DisplayList(env.ReplyTo),
		Subject: msg.Subject,
		Headers: msg.Headers,
	}
	if useTemplate {
		req.Template = &template{ID: msg.Template.ID, Variables: msg.Template.Map()}
	} else {
		req.HTML, req.Text = msg.HTML, msg.Text
	}
	for _, k := range slices.Sorted(maps.Keys(msg.Metadata)) {
		v := msg.Metadata[k]
		if !emailutil.ValidTag(k) || !emailutil.ValidTag(v) {
			return nil, notijet.Invalid(Name, "Metadata "+strconv.Quote(k)+": Resend tags allow only ASCII letters, digits, _ and - (up to 256)")
		}
		req.Tags = append(req.Tags, tag{Name: k, Value: v})
	}
	for _, a := range msg.Attachments {
		att := attachment{Filename: a.Filename, Content: base64.StdEncoding.EncodeToString(a.Content), ContentType: mimex.ContentType(a)}
		if a.Inline() {
			att.ContentID = strings.Trim(a.ContentID, "<>")
			if att.Filename == "" {
				att.Filename = att.ContentID
			}
		}
		req.Attachments = append(req.Attachments, att)
	}

	r, err := httpc.JSON(http.MethodPost, p.baseURL+"/emails", req)
	if err != nil {
		return nil, notijet.Fault(Name, "encoding request failed", err)
	}
	r.Set("Authorization", "Bearer "+p.apiKey)
	if msg.ID != "" {
		r.Set("Idempotency-Key", msg.ID)
	}
	resp, err := p.client.Do(ctx, r)
	if err != nil {
		// Resend uses 429 for spent quotas as well as for the per-second rate
		// limit; only the latter clears by waiting a moment.
		var e errorReply
		if resp != nil && errors.Is(err, notijet.ErrRateLimited) && json.Unmarshal(resp.Body, &e) == nil && e.Name != "" {
			return nil, mapError(resp.Status, e)
		}
		return nil, err
	}
	if !resp.OK() {
		var e errorReply
		if json.Unmarshal(resp.Body, &e) != nil || e.Name == "" {
			e = errorReply{Message: resp.Snippet()}
		}
		return nil, mapError(resp.Status, e)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := p.client.Decode(resp, &out); err != nil {
		return nil, err
	}
	if out.ID == "" {
		return nil, p.client.Unexpected(resp, "")
	}
	return &notijet.Result{Provider: Name, Receipts: emailutil.Receipt(out.ID, "accepted")}, nil
}

// mapError turns a Resend error reply into a notijet error. The code is the
// error name when there is one, else the HTTP status.
func mapError(status int, e errorReply) error {
	code := e.Name
	if code == "" {
		code = strconv.Itoa(status)
	}
	lower := strings.ToLower(e.Message)
	switch e.Name {
	case "missing_api_key", "invalid_api_key", "restricted_api_key", "suspended_api_key", "invalid_permission":
		return notijet.Rejected(Name, code, e.Message, notijet.ErrUnauthorized)
	case "rate_limit_exceeded":
		return notijet.Throttled(Name, code, e.Message)
	case "daily_quota_exceeded", "monthly_quota_exceeded":
		return notijet.Rejected(Name, code, e.Message, nil)
	case "invalid_from_address":
		return notijet.Rejected(Name, code, e.Message, notijet.ErrInvalidSender)
	case "concurrent_idempotent_requests":
		// Another request with this key is in flight; asking again shortly
		// returns its outcome.
		return notijet.FaultCode(Name, code, e.Message, nil)
	}
	switch {
	case strings.Contains(lower, "template"):
		return notijet.Rejected(Name, code, e.Message, notijet.ErrTemplate)
	case strings.Contains(lower, "domain is not verified") || strings.Contains(lower, "`from`"):
		return notijet.Rejected(Name, code, e.Message, notijet.ErrInvalidSender)
	case strings.Contains(lower, "`to`"):
		return notijet.Rejected(Name, code, e.Message, notijet.ErrInvalidRecipient)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return notijet.Rejected(Name, code, e.Message, notijet.ErrUnauthorized)
	}
	return notijet.Rejected(Name, code, e.Message, nil)
}
