// Package slack is a notijet chat provider for Slack incoming webhooks
// (https://api.slack.com/messaging/webhooks).
//
//	p := slack.New("https://hooks.slack.com/services/T000/B000/XXXX")
//	p.Send(ctx, &notijet.Message{Subject: "Deploy", Text: "*api* v1.4.2 is live"})
//
// An incoming webhook posts to the channel it was created for, so a message
// with recipients is unsupported. Text is sent as Slack mrkdwn; HTML bodies
// are unsupported. Message.Subject, when set, becomes the first line, and
// Message.Data["blocks"] may carry a Block Kit JSON array, with Text as the
// notification fallback. The webhook URL is a secret and never appears in
// errors.
package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/chatutil"
	"github.com/hatami57/notijet/internal/httpc"
)

// Name is the provider name.
const Name = "slack"

// Provider posts messages to a Slack incoming webhook.
type Provider struct {
	url    string
	client *httpc.Client
	hc     *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// New returns a provider posting to webhookURL.
func New(webhookURL string, opts ...Option) *Provider {
	p := &Provider{url: strings.TrimSpace(webhookURL)}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelChat }

type request struct {
	Text   string          `json:"text"`
	Blocks json.RawMessage `json:"blocks,omitempty"`
}

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if err := chatutil.CheckWebhook(Name, msg); err != nil {
		return nil, err
	}
	if p.url == "" {
		return nil, notijet.Invalid(Name, "webhook URL is empty")
	}
	req := request{Text: chatutil.Text(msg)}
	if b := strings.TrimSpace(msg.Data["blocks"]); b != "" {
		if !json.Valid([]byte(b)) || b[0] != '[' {
			return nil, notijet.Invalid(Name, `Data["blocks"] must be a JSON array of Block Kit blocks`)
		}
		req.Blocks = json.RawMessage(b)
	}
	r, err := httpc.JSON(http.MethodPost, p.url, req)
	if err != nil {
		return nil, notijet.Fault(Name, "encoding request failed", err)
	}
	r.Set("Accept", "text/plain")
	resp, err := p.client.Do(ctx, r)
	if err != nil {
		return nil, err
	}
	body := strings.TrimSpace(string(resp.Body))
	if !resp.OK() {
		return nil, mapError(resp.Status, body)
	}
	if body != "ok" {
		return nil, p.client.Unexpected(resp, "")
	}
	return &notijet.Result{Provider: Name, Receipts: []notijet.Receipt{{Status: "ok"}}}, nil
}

// mapError turns a Slack webhook error (a plain-text code such as
// "channel_not_found") into a notijet error.
func mapError(status int, body string) error {
	code := body
	if code == "" || strings.ContainsAny(code, " <") {
		code = strconv.Itoa(status)
	}
	switch code {
	case "no_service", "no_service_id", "no_team", "team_disabled", "invalid_token":
		return notijet.Rejected(Name, code, body, notijet.ErrUnauthorized)
	case "channel_not_found", "channel_is_archived", "user_not_found", "action_prohibited", "posting_to_general_channel_denied":
		return notijet.Rejected(Name, code, body, notijet.ErrInvalidRecipient)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return notijet.Rejected(Name, code, body, notijet.ErrUnauthorized)
	}
	return notijet.Rejected(Name, code, body, nil)
}
