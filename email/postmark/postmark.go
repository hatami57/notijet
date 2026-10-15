// Package postmark is a notijet email provider for Postmark
// (https://postmarkapp.com/developer/api/email-api).
//
//	p := postmark.New(serverToken, postmark.WithFrom("Acme <no-reply@acme.com>"))
//	p.Send(ctx, &notijet.Message{To: []string{"user@example.com"}, Subject: "Hi", HTML: "<p>Hi</p>"})
//
// A message with a Template goes through /email/withTemplate: a numeric
// Template.ID is the template ID, any other value its alias, and the params
// form the TemplateModel. Metadata is sent as Postmark metadata. Postmark
// accepts at most 50 recipients (To, Cc and Bcc together) per message.
package postmark

import (
	"context"
	"encoding/base64"
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
const Name = "postmark"

// DefaultBaseURL is Postmark's API root.
const DefaultBaseURL = "https://api.postmarkapp.com"

// MaxRecipients is the most recipients Postmark accepts per message.
const MaxRecipients = 50

// Provider sends email through Postmark.
type Provider struct {
	token   string
	from    string
	stream  string
	baseURL string
	client  *httpc.Client
	hc      *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithFrom sets the From address used when Message.From is empty.
func WithFrom(from string) Option { return func(p *Provider) { p.from = from } }

// WithMessageStream sets the message stream (default: the server's
// transactional stream, "outbound").
func WithMessageStream(stream string) Option { return func(p *Provider) { p.stream = stream } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Postmark provider for a server API token.
func New(serverToken string, opts ...Option) *Provider {
	p := &Provider{token: strings.TrimSpace(serverToken), baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelEmail }

type header struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type attachment struct {
	Name        string `json:"Name"`
	Content     string `json:"Content"`
	ContentType string `json:"ContentType"`
	ContentID   string `json:"ContentID,omitempty"`
}

type request struct {
	From          string            `json:"From"`
	To            string            `json:"To"`
	Cc            string            `json:"Cc,omitempty"`
	Bcc           string            `json:"Bcc,omitempty"`
	ReplyTo       string            `json:"ReplyTo,omitempty"`
	Subject       string            `json:"Subject,omitempty"`
	TextBody      string            `json:"TextBody,omitempty"`
	HtmlBody      string            `json:"HtmlBody,omitempty"`
	Headers       []header          `json:"Headers,omitempty"`
	Attachments   []attachment      `json:"Attachments,omitempty"`
	Metadata      map[string]string `json:"Metadata,omitempty"`
	MessageStream string            `json:"MessageStream,omitempty"`
	TemplateID    int64             `json:"TemplateId,omitempty"`
	TemplateAlias string            `json:"TemplateAlias,omitempty"`
	TemplateModel map[string]string `json:"TemplateModel,omitempty"`
}

type reply struct {
	MessageID string `json:"MessageID"`
	ErrorCode int    `json:"ErrorCode"`
	Message   string `json:"Message"`
}

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	env, useTemplate, err := emailutil.Prepare(Name, msg, p.from, true)
	if err != nil {
		return nil, err
	}
	if p.token == "" {
		return nil, notijet.Invalid(Name, "server token is empty")
	}
	if env.Count() > MaxRecipients {
		return nil, notijet.Invalid(Name, "Postmark accepts at most 50 recipients per message")
	}

	req := request{
		From:          emailutil.Display(env.From),
		To:            strings.Join(emailutil.DisplayList(env.To), ", "),
		Cc:            strings.Join(emailutil.DisplayList(env.CC), ", "),
		Bcc:           strings.Join(emailutil.DisplayList(env.BCC), ", "),
		ReplyTo:       strings.Join(emailutil.DisplayList(env.ReplyTo), ", "),
		Subject:       msg.Subject,
		Metadata:      msg.Metadata,
		MessageStream: p.stream,
	}
	for _, k := range slices.Sorted(maps.Keys(msg.Headers)) {
		req.Headers = append(req.Headers, header{Name: k, Value: msg.Headers[k]})
	}
	for _, a := range msg.Attachments {
		att := attachment{Name: a.Filename, Content: base64.StdEncoding.EncodeToString(a.Content), ContentType: mimex.ContentType(a)}
		if a.Inline() {
			cid := strings.Trim(a.ContentID, "<>")
			att.ContentID = "cid:" + cid
			if att.Name == "" {
				att.Name = cid
			}
		}
		req.Attachments = append(req.Attachments, att)
	}

	path := "/email"
	if useTemplate {
		path = "/email/withTemplate"
		req.Subject = ""
		if id, err := strconv.ParseInt(msg.Template.ID, 10, 64); err == nil {
			req.TemplateID = id
		} else {
			req.TemplateAlias = msg.Template.ID
		}
		req.TemplateModel = msg.Template.Map()
	} else {
		req.TextBody, req.HtmlBody = msg.Text, msg.HTML
	}

	r, err := httpc.JSON(http.MethodPost, p.baseURL+path, req)
	if err != nil {
		return nil, notijet.Fault(Name, "encoding request failed", err)
	}
	resp, err := p.client.Do(ctx, r.Set("X-Postmark-Server-Token", p.token))
	if err != nil {
		return nil, err
	}
	var out reply
	if err := p.client.Decode(resp, &out); err != nil {
		if resp.Status == http.StatusUnauthorized {
			return nil, mapError(10, "unauthorized")
		}
		return nil, err
	}
	if out.ErrorCode != 0 || !resp.OK() {
		code := out.ErrorCode
		if code == 0 {
			return nil, p.client.Unexpected(resp, "")
		}
		return nil, mapError(code, out.Message)
	}
	if out.MessageID == "" {
		return nil, p.client.Unexpected(resp, "")
	}
	return &notijet.Result{Provider: Name, Receipts: emailutil.Receipt(out.MessageID, "accepted")}, nil
}

// mapError turns a Postmark ErrorCode into a notijet error.
func mapError(code int, message string) error {
	c := strconv.Itoa(code)
	switch code {
	case 10, 413, 1480:
		return notijet.Rejected(Name, c, message, notijet.ErrUnauthorized)
	case 400, 401:
		return notijet.Rejected(Name, c, message, notijet.ErrInvalidSender)
	case 405:
		return notijet.Rejected(Name, c, message, notijet.ErrInsufficientCredit)
	case 406:
		return notijet.Rejected(Name, c, message, notijet.ErrInvalidRecipient)
	case 1101, 1109, 1122, 1123:
		return notijet.Rejected(Name, c, message, notijet.ErrTemplate)
	case 100, 101:
		return notijet.FaultCode(Name, c, message, nil)
	}
	return notijet.Rejected(Name, c, message, nil)
}
