// Package mailgun is a notijet email provider for the Mailgun Messages API
// (https://documentation.mailgun.com/docs/mailgun/api-reference/send/mailgun/messages).
//
//	p := mailgun.New("mg.acme.com", apiKey, mailgun.WithFrom("Acme <no-reply@mg.acme.com>"))
//	p.Send(ctx, &notijet.Message{To: []string{"user@example.com"}, Subject: "Hi", Text: "Hi"})
//
// Use WithBaseURL(EUBaseURL) for a domain in Mailgun's EU region. A message
// with a Template is sent with the stored template named by Template.ID and
// its params as t:variables. Metadata is sent as v: custom variables, which
// Mailgun attaches to events and webhooks. An inline attachment is referenced
// from the HTML as cid:<ContentID>.
package mailgun

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/emailutil"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/mimex"
)

// Name is the provider name.
const Name = "mailgun"

// API roots.
const (
	DefaultBaseURL = "https://api.mailgun.net"
	EUBaseURL      = "https://api.eu.mailgun.net"
)

// Provider sends email through Mailgun.
type Provider struct {
	domain  string
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

// WithBaseURL overrides the API root (EUBaseURL for the EU region, or a test
// server).
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Mailgun provider sending from domain with apiKey.
func New(domain, apiKey string, opts ...Option) *Provider {
	p := &Provider{domain: strings.TrimSpace(domain), apiKey: strings.TrimSpace(apiKey), baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
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
	if p.domain == "" || p.apiKey == "" {
		return nil, notijet.Invalid(Name, "domain and API key are required")
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	field := func(k, v string) { _ = w.WriteField(k, v) }
	field("from", emailutil.Display(env.From))
	for _, a := range env.To {
		field("to", emailutil.Display(a))
	}
	for _, a := range env.CC {
		field("cc", emailutil.Display(a))
	}
	for _, a := range env.BCC {
		field("bcc", emailutil.Display(a))
	}
	if msg.Subject != "" {
		field("subject", msg.Subject)
	}
	if len(env.ReplyTo) > 0 {
		field("h:Reply-To", strings.Join(emailutil.DisplayList(env.ReplyTo), ", "))
	}
	if useTemplate {
		field("template", msg.Template.ID)
		vars, _ := json.Marshal(msg.Template.Map())
		field("t:variables", string(vars))
	} else {
		if msg.Text != "" {
			field("text", msg.Text)
		}
		if msg.HTML != "" {
			field("html", msg.HTML)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(msg.Headers)) {
		field("h:"+k, msg.Headers[k])
	}
	for _, k := range slices.Sorted(maps.Keys(msg.Metadata)) {
		field("v:"+k, msg.Metadata[k])
	}
	for _, a := range msg.Attachments {
		name, kind := a.Filename, "attachment"
		if a.Inline() {
			// Mailgun names the Content-ID of an inline part after its file
			// name, so the file name is what cid: refers to.
			name, kind = strings.Trim(a.ContentID, "<>"), "inline"
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+kind+`"; filename="`+escapeQuotes(name)+`"`)
		h.Set("Content-Type", mimex.ContentType(a))
		part, _ := w.CreatePart(h)
		_, _ = part.Write(a.Content)
	}
	_ = w.Close()

	endpoint := p.baseURL + "/v3/" + url.PathEscape(p.domain) + "/messages"
	req := httpc.NewRequest(http.MethodPost, endpoint).Set("Content-Type", w.FormDataContentType()).BasicAuth("api", p.apiKey)
	req.Body = body.Bytes()
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	var r struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if err := p.client.Decode(resp, &r); err != nil {
		if !resp.OK() {
			return nil, mapError(resp.Status, resp.Snippet())
		}
		return nil, err
	}
	if !resp.OK() {
		return nil, mapError(resp.Status, r.Message)
	}
	if r.ID == "" {
		return nil, p.client.Unexpected(resp, "")
	}
	return &notijet.Result{Provider: Name, Receipts: emailutil.Receipt(r.ID, "queued")}, nil
}

func escapeQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(s)
}

// mapError turns a Mailgun error reply into a notijet error.
func mapError(status int, message string) error {
	code := strconv.Itoa(status)
	lower := strings.ToLower(message)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case status == http.StatusPaymentRequired:
		return notijet.Rejected(Name, code, message, notijet.ErrInsufficientCredit)
	case strings.Contains(lower, "template"):
		return notijet.Rejected(Name, code, message, notijet.ErrTemplate)
	case status == http.StatusNotFound || strings.Contains(lower, "'from'") || strings.Contains(lower, "domain"):
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
	case strings.Contains(lower, "'to'") || strings.Contains(lower, "'cc'") || strings.Contains(lower, "'bcc'"):
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
	}
	return notijet.Rejected(Name, code, message, nil)
}
