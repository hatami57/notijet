// Package webhook is a notijet provider that POSTs each message as JSON to an
// HTTP endpoint you control, signed with HMAC-SHA256 — for routing
// notifications into your own services, automation tools or anything else
// that speaks HTTP.
//
//	p := webhook.New("https://hooks.acme.com/notify", webhook.WithSecret(secret))
//	p.Send(ctx, &notijet.Message{To: []string{"ops"}, Subject: "Disk full", Text: "db-1 at 95%"})
//
// The body is a Payload carrying every message field. With a secret, each
// request has an X-Notijet-Timestamp header (Unix seconds) and an
// X-Notijet-Signature header, "sha256=" + hex(HMAC-SHA256(secret,
// timestamp + "." + body)); receivers check both with a Verifier. Any 2xx
// reply is a success; a JSON reply with an "id" field sets the receipt's
// MessageID.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/httpc"
)

// Name is the provider name.
const Name = "webhook"

// Headers set on every signed request.
const (
	TimestampHeader = "X-Notijet-Timestamp"
	SignatureHeader = "X-Notijet-Signature"
)

// Payload is the JSON body the provider sends.
type Payload struct {
	ID          string            `json:"id,omitempty"`
	From        string            `json:"from,omitempty"`
	To          []string          `json:"to,omitempty"`
	CC          []string          `json:"cc,omitempty"`
	BCC         []string          `json:"bcc,omitempty"`
	ReplyTo     []string          `json:"reply_to,omitempty"`
	Subject     string            `json:"subject,omitempty"`
	Text        string            `json:"text,omitempty"`
	HTML        string            `json:"html,omitempty"`
	Template    *Template         `json:"template,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Data        map[string]string `json:"data,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// SentAt is when the provider sent the request.
	SentAt time.Time `json:"sent_at"`
}

// Template is a provider template reference with ordered params.
type Template struct {
	ID     string  `json:"id"`
	Params []Param `json:"params,omitempty"`
}

// Param is one named template value.
type Param struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Attachment is a file, its content base64-encoded in JSON.
type Attachment struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Content     []byte `json:"content"`
	ContentID   string `json:"content_id,omitempty"`
}

// NewPayload converts msg to the wire form.
func NewPayload(msg *notijet.Message, sentAt time.Time) Payload {
	p := Payload{
		ID: msg.ID, From: msg.From,
		To: msg.Recipients(), CC: notijet.CleanList(msg.CC), BCC: notijet.CleanList(msg.BCC), ReplyTo: notijet.CleanList(msg.ReplyTo),
		Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML,
		Headers: msg.Headers, Data: msg.Data, Metadata: msg.Metadata,
		SentAt: sentAt.UTC().Truncate(time.Second),
	}
	if t := msg.Template; t != nil {
		p.Template = &Template{ID: t.ID}
		for _, prm := range t.Params {
			p.Template.Params = append(p.Template.Params, Param(prm))
		}
	}
	for _, a := range msg.Attachments {
		p.Attachments = append(p.Attachments, Attachment{Filename: a.Filename, ContentType: a.ContentType, Content: a.Content, ContentID: a.ContentID})
	}
	return p
}

// Message converts the payload back to a notijet.Message, for receivers that
// pass it on to another provider.
func (p *Payload) Message() *notijet.Message {
	m := &notijet.Message{
		ID: p.ID, From: p.From, To: p.To, CC: p.CC, BCC: p.BCC, ReplyTo: p.ReplyTo,
		Subject: p.Subject, Text: p.Text, HTML: p.HTML,
		Headers: p.Headers, Data: p.Data, Metadata: p.Metadata,
	}
	if p.Template != nil {
		m.Template = &notijet.Template{ID: p.Template.ID}
		for _, prm := range p.Template.Params {
			m.Template.Params = append(m.Template.Params, notijet.Param(prm))
		}
	}
	for _, a := range p.Attachments {
		m.Attachments = append(m.Attachments, notijet.Attachment(a))
	}
	return m
}

// Provider posts messages to a webhook endpoint.
type Provider struct {
	url     string
	secret  []byte
	headers map[string]string
	channel notijet.Channel
	now     func() time.Time
	client  *httpc.Client
	hc      *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithSecret signs every request with secret (see the package documentation).
func WithSecret(secret string) Option { return func(p *Provider) { p.secret = []byte(secret) } }

// WithHeader adds a static header to every request, such as an
// Authorization token the endpoint expects.
func WithHeader(key, value string) Option {
	return func(p *Provider) {
		if p.headers == nil {
			p.headers = map[string]string{}
		}
		p.headers[key] = value
	}
}

// WithChannel sets the channel the provider reports (default
// notijet.ChannelWebhook), to register it in a Notifier as, say, the chat
// provider.
func WithChannel(ch notijet.Channel) Option { return func(p *Provider) { p.channel = ch } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// New returns a provider posting to endpoint.
func New(endpoint string, opts ...Option) *Provider {
	p := &Provider{url: strings.TrimSpace(endpoint), channel: notijet.ChannelWebhook, now: time.Now}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return p.channel }

// Send posts msg; see the package documentation. Recipients are optional and
// passed through as they are.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	switch {
	case msg == nil:
		return nil, notijet.Invalid(Name, "message is nil")
	case !msg.HasBody() && len(msg.Data) == 0:
		return nil, notijet.Invalid(Name, "message has no Text, HTML, Template or Data")
	case msg.Template != nil && strings.TrimSpace(msg.Template.ID) == "":
		return nil, notijet.Invalid(Name, "Template.ID is required")
	case p.url == "":
		return nil, notijet.Invalid(Name, "endpoint URL is empty")
	}
	now := p.now()
	body, err := json.Marshal(NewPayload(msg, now))
	if err != nil {
		return nil, notijet.Fault(Name, "encoding payload failed", err)
	}
	req := httpc.NewRequest(http.MethodPost, p.url).Set("Content-Type", "application/json")
	req.Body = body
	for k, v := range p.headers {
		req.Set(k, v)
	}
	if len(p.secret) > 0 {
		ts := strconv.FormatInt(now.Unix(), 10)
		req.Set(TimestampHeader, ts)
		req.Set(SignatureHeader, Sign(p.secret, ts, body))
	}
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, mapError(resp.Status, resp.Snippet())
	}
	var out struct {
		ID json.RawMessage `json:"id"`
	}
	rc := notijet.Receipt{Status: strconv.Itoa(resp.Status)}
	if json.Unmarshal(resp.Body, &out) == nil && len(out.ID) > 0 {
		var s string
		if json.Unmarshal(out.ID, &s) != nil {
			s = string(out.ID) // a numeric ID
		}
		rc.MessageID = s
	}
	return &notijet.Result{Provider: Name, Receipts: []notijet.Receipt{rc}}, nil
}

// mapError turns a non-2xx reply (429 and 5xx are handled before) into a
// notijet error. A 408 is transient; other 4xx are permanent.
func mapError(status int, body string) error {
	code := strconv.Itoa(status)
	message := "endpoint returned HTTP " + code
	if body != "" {
		message += ": " + body
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case http.StatusRequestTimeout:
		return notijet.FaultCode(Name, code, message, nil)
	}
	return notijet.Rejected(Name, code, message, nil)
}

// Sign returns the signature header value for a request body sent at
// timestamp (Unix seconds, as sent in TimestampHeader).
func Sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Errors a Verifier returns.
var (
	ErrInvalidRequest   = errors.New("webhook: request or body is nil")
	ErrMissingSignature = errors.New("webhook: missing signature or timestamp header")
	ErrBadSignature     = errors.New("webhook: signature mismatch")
	ErrStaleTimestamp   = errors.New("webhook: timestamp outside the allowed window")
	// ErrNoSecret means the Verifier has no secret: anyone could sign with an
	// empty key, so nothing is accepted.
	ErrNoSecret = errors.New("webhook: verifier has no secret")
)

// DefaultTolerance is how far a request's timestamp may be from the
// receiver's clock.
const DefaultTolerance = 5 * time.Minute

// maxBody caps how much of a request a Verifier reads.
const maxBody = 32 << 20

// Verifier checks signed webhook requests on the receiving side:
//
//	v := webhook.Verifier{Secret: []byte(secret)}
//	http.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
//	    payload, err := v.Verify(r)
//	    if err != nil {
//	        http.Error(w, "forbidden", http.StatusForbidden)
//	        return
//	    }
//	    ...
//	})
type Verifier struct {
	Secret []byte
	// Tolerance bounds the age (and clock skew) of a request; zero means
	// DefaultTolerance. It limits how long a captured request can be replayed.
	Tolerance time.Duration
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
}

// Verify reads r's body, checks its timestamp and signature, and decodes the
// payload.
func (v Verifier) Verify(r *http.Request) (*Payload, error) {
	if r == nil || r.Body == nil {
		return nil, ErrInvalidRequest
	}
	ts, sig := r.Header.Get(TimestampHeader), r.Header.Get(SignatureHeader)
	if ts == "" || sig == "" {
		return nil, ErrMissingSignature
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return v.VerifyBody(ts, sig, body)
}

// VerifyBody checks a request already read: its timestamp and signature
// header values and its body.
func (v Verifier) VerifyBody(timestamp, signature string, body []byte) (*Payload, error) {
	if len(v.Secret) == 0 {
		return nil, ErrNoSecret
	}
	if timestamp == "" || signature == "" {
		return nil, ErrMissingSignature
	}
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return nil, ErrStaleTimestamp
	}
	now, tol := time.Now, v.Tolerance
	if v.Now != nil {
		now = v.Now
	}
	if tol <= 0 {
		tol = DefaultTolerance
	}
	if d := now().Sub(time.Unix(sec, 0)); d > tol || d < -tol {
		return nil, ErrStaleTimestamp
	}
	if !hmac.Equal([]byte(signature), []byte(Sign(v.Secret, timestamp, body))) {
		return nil, ErrBadSignature
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
