// Package gmail is a notijet email provider for the Gmail API
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/send).
//
// The Gmail API authenticates with OAuth2 (scope
// https://www.googleapis.com/auth/gmail.send). Pass an HTTP client that adds
// the token — for example one from golang.org/x/oauth2 — or a function that
// returns a fresh access token:
//
//	hc := oauthConfig.Client(ctx, token) // golang.org/x/oauth2
//	p := gmail.New(gmail.WithHTTPClient(hc), gmail.WithFrom("Me <me@gmail.com>"))
//	p.Send(ctx, &notijet.Message{To: []string{"user@example.com"}, Subject: "Hi", Text: "Hi"})
//
// The message is built by notijet's MIME builder, Bcc header included (Gmail
// removes it before delivery), and uploaded as message/rfc822, so messages up
// to Gmail's 35 MB limit work. Gmail sends from the authenticated account or
// one of its verified aliases, whatever From says. For Gmail over SMTP, use
// email/smtp with the Gmail preset.
package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/mimex"
)

// Name is the provider name.
const Name = "gmail"

// DefaultBaseURL is the Gmail API root.
const DefaultBaseURL = "https://gmail.googleapis.com"

// Provider sends email through the Gmail API.
type Provider struct {
	user    string
	from    string
	token   func(ctx context.Context) (string, error)
	baseURL string
	client  *httpc.Client
	hc      *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithHTTPClient sets the HTTP client. Without WithToken it must add the
// OAuth2 token itself (an oauth2.Client does).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithToken sets a function returning a fresh OAuth2 access token, sent as a
// Bearer token on every request — plug in an oauth2.TokenSource there.
func WithToken(token func(ctx context.Context) (string, error)) Option {
	return func(p *Provider) { p.token = token }
}

// WithUser sets the mailbox to send as (default "me", the authenticated
// user); set an address when a service account impersonates users.
func WithUser(user string) Option { return func(p *Provider) { p.user = user } }

// WithFrom sets the From address used when Message.From is empty.
func WithFrom(from string) Option { return func(p *Provider) { p.from = from } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Gmail API provider. It needs WithHTTPClient (an
// OAuth2-authenticated client) or WithToken.
func New(opts ...Option) *Provider {
	p := &Provider{user: "me", baseURL: DefaultBaseURL}
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
	if _, err := notijet.CheckEmail(Name, msg, false); err != nil {
		return nil, err
	}
	if p.hc == nil && p.token == nil {
		return nil, notijet.Invalid(Name, "no credentials: set WithHTTPClient (OAuth2 client) or WithToken")
	}
	built, err := mimex.Build(Name, msg, mimex.Options{From: p.from, IncludeBCC: true})
	if err != nil {
		return nil, err
	}

	endpoint := p.baseURL + "/upload/gmail/v1/users/" + url.PathEscape(p.user) + "/messages/send?uploadType=media"
	req := httpc.NewRequest(http.MethodPost, endpoint).Set("Content-Type", "message/rfc822")
	req.Body = built.Bytes
	if p.token != nil {
		tok, err := p.token(ctx)
		if err != nil {
			return nil, notijet.Fault(Name, "fetching OAuth2 token failed", err)
		}
		req.Set("Authorization", "Bearer "+tok)
	}
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, mapError(resp.Status, resp.Body)
	}
	var out struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	if err := p.client.Decode(resp, &out); err != nil {
		return nil, err
	}
	if out.ID == "" {
		return nil, p.client.Unexpected(resp, "")
	}
	res := &notijet.Result{Provider: Name}
	for _, rcpt := range built.Recipients {
		res.Receipts = append(res.Receipts, notijet.Receipt{To: rcpt, MessageID: out.ID, Status: "sent"})
	}
	return res, nil
}

type errorReply struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

// mapError turns a Google API error reply into a notijet error. The code is
// the first error reason when there is one, else the HTTP status.
func mapError(status int, body []byte) error {
	var r errorReply
	_ = json.Unmarshal(body, &r)
	message := r.Error.Message
	if message == "" {
		message = httpc.Snippet(body)
	}
	code := strconv.Itoa(status)
	if len(r.Error.Errors) > 0 && r.Error.Errors[0].Reason != "" {
		code = r.Error.Errors[0].Reason
	}
	lower := strings.ToLower(message)
	switch {
	case code == "rateLimitExceeded" || code == "userRateLimitExceeded":
		return notijet.Throttled(Name, code, message)
	case code == "dailyLimitExceeded" || code == "quotaExceeded":
		return notijet.Rejected(Name, code, message, nil)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case strings.Contains(lower, "invalid to") || strings.Contains(lower, "recipient address"):
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
	case strings.Contains(lower, "invalid from"):
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
	}
	return notijet.Rejected(Name, code, message, nil)
}
