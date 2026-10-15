// Package vonage is a notijet SMS provider for the Vonage (formerly Nexmo)
// SMS API (https://developer.vonage.com/en/api/sms).
//
// Each recipient gets its own request. Text outside the GSM 7-bit alphabet
// (Persian, Arabic, emoji ...) is sent as a unicode message automatically.
//
//	p := vonage.New(apiKey, apiSecret, vonage.WithSender("Acme"))
//	p.Send(ctx, &notijet.Message{To: []string{"+447700900123"}, Text: "Your code is 1234"})
//
// Message.ID is forwarded as client-ref (first 100 characters), which Vonage
// echoes in delivery receipts.
package vonage

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/flexjson"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/smsutil"
	"github.com/hatami57/notijet/phone"
)

// Name is the provider name.
const Name = "vonage"

// DefaultBaseURL is the Vonage SMS API root.
const DefaultBaseURL = "https://rest.nexmo.com"

// maxClientRef is the longest client-ref Vonage accepts.
const maxClientRef = 100

// Provider sends SMS through Vonage.
type Provider struct {
	apiKey, apiSecret string
	sender            string
	country           string
	callback          string
	baseURL           string
	client            *httpc.Client
	hc                *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithSender sets the default sender (a number or an alphanumeric ID), used
// when Message.From is empty.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithDefaultCountry sets the calling code ("44", "1") assumed for numbers
// written in national form with a leading 0.
func WithDefaultCountry(code string) Option { return func(p *Provider) { p.country = code } }

// WithCallback sets the URL Vonage sends delivery receipts to, overriding the
// account default.
func WithCallback(u string) Option { return func(p *Provider) { p.callback = u } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Vonage provider for an API key and secret.
func New(apiKey, apiSecret string, opts ...Option) *Provider {
	p := &Provider{apiKey: strings.TrimSpace(apiKey), apiSecret: apiSecret, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelSMS }

type reply struct {
	Messages []struct {
		To           string          `json:"to"`
		MessageID    string          `json:"message-id"`
		Status       flexjson.String `json:"status"`
		ErrorText    string          `json:"error-text"`
		MessagePrice flexjson.String `json:"message-price"`
	} `json:"messages"`
}

// Send delivers msg, one request per recipient.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if _, err := notijet.CheckSMS(Name, msg, false); err != nil {
		return nil, err
	}
	if p.apiKey == "" || p.apiSecret == "" {
		return nil, notijet.Invalid(Name, "API key and secret are required")
	}
	to, err := smsutil.Recipients(Name, msg, func(s string) (string, error) { return phone.International(s, p.country) })
	if err != nil {
		return nil, err
	}
	sender, err := smsutil.Sender(Name, msg, p.sender, true)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"api_key":    {p.apiKey},
		"api_secret": {p.apiSecret},
		"from":       {sender},
		"text":       {msg.Text},
	}
	if !smsutil.IsGSM7(msg.Text) {
		form.Set("type", "unicode")
	}
	if ref := msg.ID; ref != "" {
		if r := []rune(ref); len(r) > maxClientRef {
			ref = string(r[:maxClientRef])
		}
		form.Set("client-ref", ref)
	}
	if p.callback != "" {
		form.Set("callback", p.callback)
	}
	return notijet.SendBatches(ctx, Name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		form.Set("to", batch[0])
		resp, err := p.client.Do(ctx, httpc.Form(http.MethodPost, p.baseURL+"/sms/json", form))
		if err != nil {
			return nil, err
		}
		var r reply
		if err := p.client.Decode(resp, &r); err != nil {
			if resp.Status == http.StatusUnauthorized {
				return nil, notijet.Rejected(Name, "401", "authentication failed", notijet.ErrUnauthorized)
			}
			return nil, err
		}
		if len(r.Messages) == 0 {
			return nil, p.client.Unexpected(resp, "")
		}
		// A long text is split into parts, each reported separately; the
		// first part's ID identifies the message and any failed part fails it.
		for _, m := range r.Messages {
			if m.Status != "0" {
				return nil, mapError(string(m.Status), m.ErrorText)
			}
		}
		first := r.Messages[0]
		return []notijet.Receipt{{To: "+" + batch[0], MessageID: first.MessageID, Cost: string(first.MessagePrice)}}, nil
	})
}

// mapError turns a Vonage message status into a notijet error.
func mapError(status, message string) error {
	code, _ := strconv.Atoi(status)
	switch code {
	case 1:
		return notijet.Throttled(Name, status, message)
	case 4, 8, 11, 14, 32:
		return notijet.Rejected(Name, status, message, notijet.ErrUnauthorized)
	case 5:
		return notijet.FaultCode(Name, status, message, nil)
	case 6, 7, 29, 33:
		return notijet.Rejected(Name, status, message, notijet.ErrInvalidRecipient)
	case 9:
		return notijet.Rejected(Name, status, message, notijet.ErrInsufficientCredit)
	case 15:
		return notijet.Rejected(Name, status, message, notijet.ErrInvalidSender)
	}
	return notijet.Rejected(Name, status, message, nil)
}
