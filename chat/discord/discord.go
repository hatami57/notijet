// Package discord is a notijet chat provider for Discord webhooks
// (https://discord.com/developers/docs/resources/webhook#execute-webhook).
//
//	p := discord.New("https://discord.com/api/webhooks/123/XXXX", discord.WithUsername("Deploy bot"))
//	p.Send(ctx, &notijet.Message{Subject: "Deploy", Text: "**api** v1.4.2 is live"})
//
// A webhook posts to the channel it was created for, so a message with
// recipients is unsupported. Text is Discord markdown, up to 2000 characters;
// HTML bodies are unsupported. Message.Subject, when set, becomes the first
// line, and Message.Data["embeds"] may carry a JSON array of embeds. Mentions
// in the text (@everyone, roles, users) do not ping anyone unless
// WithMentions is set. The webhook URL is a secret and never appears in
// errors.
package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/chatutil"
	"github.com/hatami57/notijet/internal/flexjson"
	"github.com/hatami57/notijet/internal/httpc"
)

// Name is the provider name.
const Name = "discord"

// MaxContent is the longest message content, in characters, Discord accepts.
const MaxContent = 2000

// Provider posts messages to a Discord webhook.
type Provider struct {
	url       string
	username  string
	avatarURL string
	mentions  bool
	client    *httpc.Client
	hc        *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithUsername overrides the webhook's display name.
func WithUsername(name string) Option { return func(p *Provider) { p.username = name } }

// WithAvatarURL overrides the webhook's avatar.
func WithAvatarURL(u string) Option { return func(p *Provider) { p.avatarURL = u } }

// WithMentions lets mentions in the text ping the users, roles and
// @everyone/@here they name. By default they are rendered but ping no one.
func WithMentions() Option { return func(p *Provider) { p.mentions = true } }

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

type allowedMentions struct {
	Parse []string `json:"parse"`
}

type request struct {
	Content         string           `json:"content"`
	Username        string           `json:"username,omitempty"`
	AvatarURL       string           `json:"avatar_url,omitempty"`
	Embeds          json.RawMessage  `json:"embeds,omitempty"`
	AllowedMentions *allowedMentions `json:"allowed_mentions,omitempty"`
}

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if err := chatutil.CheckWebhook(Name, msg); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(p.url)
	if p.url == "" || err != nil {
		return nil, notijet.Invalid(Name, "webhook URL is empty or malformed")
	}
	req := request{Content: chatutil.Text(msg), Username: p.username, AvatarURL: p.avatarURL}
	if n := utf8.RuneCountInString(req.Content); n > MaxContent {
		return nil, notijet.Invalid(Name, "message content is "+strconv.Itoa(n)+" characters; the limit is 2000")
	}
	if !p.mentions {
		req.AllowedMentions = &allowedMentions{Parse: []string{}}
	}
	if e := strings.TrimSpace(msg.Data["embeds"]); e != "" {
		if !json.Valid([]byte(e)) || e[0] != '[' {
			return nil, notijet.Invalid(Name, `Data["embeds"] must be a JSON array of embeds`)
		}
		req.Embeds = json.RawMessage(e)
	}

	// wait=true makes Discord answer with the created message, and its ID.
	q := endpoint.Query()
	q.Set("wait", "true")
	endpoint.RawQuery = q.Encode()
	r, err := httpc.JSON(http.MethodPost, endpoint.String(), req)
	if err != nil {
		return nil, notijet.Fault(Name, "encoding request failed", err)
	}
	resp, err := p.client.Do(ctx, r)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID      string          `json:"id"`
		Message string          `json:"message"`
		Code    flexjson.String `json:"code"`
	}
	if err := p.client.Decode(resp, &out); err != nil {
		if !resp.OK() {
			return nil, mapError(resp.Status, "", resp.Snippet())
		}
		return nil, err
	}
	if !resp.OK() {
		return nil, mapError(resp.Status, string(out.Code), out.Message)
	}
	if out.ID == "" {
		return nil, p.client.Unexpected(resp, "")
	}
	return &notijet.Result{Provider: Name, Receipts: []notijet.Receipt{{MessageID: out.ID, Status: "sent"}}}, nil
}

// mapError turns a Discord error reply into a notijet error. The code is
// Discord's JSON error code when there is one, else the HTTP status.
func mapError(status int, code, message string) error {
	if code == "" || code == "0" {
		code = strconv.Itoa(status)
	}
	switch {
	case code == "10015" || code == "50027" || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound:
		// Unknown Webhook, Invalid Webhook Token: the URL is wrong or revoked.
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	}
	return notijet.Rejected(Name, code, message, nil)
}
