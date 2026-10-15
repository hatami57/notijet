// Package twilio is a notijet SMS provider for Twilio Programmable Messaging
// (https://www.twilio.com/docs/messaging/api/message-resource).
//
// Each recipient gets its own Messages request. The sender is a phone number,
// an alphanumeric sender ID, or a Messaging Service SID (MG...), which Twilio
// then uses to pick a number:
//
//	p := twilio.New(accountSID, authToken, twilio.WithSender("MG0123..."))
//	p.Send(ctx, &notijet.Message{To: []string{"+14155550100"}, Text: "Your code is 1234"})
//
// Twilio has no server-side templates here; a message with a Template needs
// a fallback Text.
package twilio

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
const Name = "twilio"

// DefaultBaseURL is Twilio's REST API root.
const DefaultBaseURL = "https://api.twilio.com/2010-04-01"

// Provider sends SMS through Twilio.
type Provider struct {
	accountSID     string
	user, password string
	sender         string
	country        string
	statusCallback string
	baseURL        string
	client         *httpc.Client
	hc             *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithSender sets the default sender — a number, an alphanumeric ID or a
// Messaging Service SID — used when Message.From is empty.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithAPIKey authenticates with an API key SID and secret instead of the
// account's auth token.
func WithAPIKey(sid, secret string) Option {
	return func(p *Provider) { p.user, p.password = sid, secret }
}

// WithDefaultCountry sets the calling code ("1", "44") assumed for numbers
// written in national form with a leading 0. Without it such numbers are
// rejected.
func WithDefaultCountry(code string) Option { return func(p *Provider) { p.country = code } }

// WithStatusCallback sets the URL Twilio posts delivery status updates to.
func WithStatusCallback(u string) Option { return func(p *Provider) { p.statusCallback = u } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a regional edge.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Twilio provider for an account SID and auth token.
func New(accountSID, authToken string, opts ...Option) *Provider {
	sid := strings.TrimSpace(accountSID)
	p := &Provider{accountSID: sid, user: sid, password: authToken, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelSMS }

type reply struct {
	SID          string          `json:"sid"`
	Status       flexjson.String `json:"status"`
	Price        flexjson.String `json:"price"`
	PriceUnit    string          `json:"price_unit"`
	Code         int             `json:"code"`
	Message      string          `json:"message"`
	ErrorCode    int             `json:"error_code"`
	ErrorMessage string          `json:"error_message"`
}

// Send delivers msg, one request per recipient.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if _, err := notijet.CheckSMS(Name, msg, false); err != nil {
		return nil, err
	}
	if p.accountSID == "" || p.user == "" || p.password == "" {
		return nil, notijet.Invalid(Name, "account SID and credentials are required")
	}
	to, err := smsutil.Recipients(Name, msg, func(s string) (string, error) { return phone.E164(s, p.country) })
	if err != nil {
		return nil, err
	}
	sender, err := smsutil.Sender(Name, msg, p.sender, true)
	if err != nil {
		return nil, err
	}

	endpoint := p.baseURL + "/Accounts/" + url.PathEscape(p.accountSID) + "/Messages.json"
	return notijet.SendBatches(ctx, Name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		form := url.Values{"To": {batch[0]}, "Body": {msg.Text}}
		if strings.HasPrefix(sender, "MG") {
			form.Set("MessagingServiceSid", sender)
		} else {
			form.Set("From", sender)
		}
		if p.statusCallback != "" {
			form.Set("StatusCallback", p.statusCallback)
		}
		resp, err := p.client.Do(ctx, httpc.Form(http.MethodPost, endpoint, form).BasicAuth(p.user, p.password))
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
		if !resp.OK() {
			return nil, mapError(resp.Status, r.Code, r.Message)
		}
		if r.SID == "" {
			return nil, p.client.Unexpected(resp, "")
		}
		if r.Status == "failed" || r.Status == "undelivered" {
			return nil, mapError(http.StatusBadRequest, r.ErrorCode, r.ErrorMessage)
		}
		cost := string(r.Price)
		if cost != "" && r.PriceUnit != "" {
			cost += " " + r.PriceUnit
		}
		return []notijet.Receipt{{To: batch[0], MessageID: r.SID, Status: string(r.Status), Cost: cost}}, nil
	})
}

// mapError turns a Twilio error reply into a notijet error.
func mapError(status, code int, message string) error {
	c := strconv.Itoa(code)
	if code == 0 {
		c = strconv.Itoa(status)
	}
	switch code {
	case 20003, 20005, 20008:
		return notijet.Rejected(Name, c, message, notijet.ErrUnauthorized)
	case 21211, 21214, 21217, 21265, 21408, 21608, 21610, 21612, 21614:
		return notijet.Rejected(Name, c, message, notijet.ErrInvalidRecipient)
	case 21212, 21603, 21606, 21659, 21660:
		return notijet.Rejected(Name, c, message, notijet.ErrInvalidSender)
	case 20429, 14107:
		return notijet.Throttled(Name, c, message)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return notijet.Rejected(Name, c, message, notijet.ErrUnauthorized)
	}
	return notijet.Rejected(Name, c, message, nil)
}
