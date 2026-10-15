// Package plivo is a notijet SMS provider for Plivo
// (https://www.plivo.com/docs/messaging/api/message).
//
// Recipients are sent in bulk, up to 1000 per request, and Plivo returns one
// message UUID per recipient. The sender is a phone number, an alphanumeric
// sender ID, or a Powerpack UUID, which Plivo then uses to pick a number:
//
//	p := plivo.New(authID, authToken, plivo.WithSender("+14155550100"))
//	p.Send(ctx, &notijet.Message{To: []string{"+447700900123"}, Text: "Your code is 1234"})
package plivo

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/smsutil"
	"github.com/hatami57/notijet/phone"
)

// Name is the provider name.
const Name = "plivo"

// DefaultBaseURL is Plivo's REST API root.
const DefaultBaseURL = "https://api.plivo.com/v1"

// maxDestinations is the most recipients sent per request.
const maxDestinations = 1000

// Provider sends SMS through Plivo.
type Provider struct {
	authID, authToken string
	sender            string
	country           string
	callback          string
	baseURL           string
	client            *httpc.Client
	hc                *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithSender sets the default sender — a number, an alphanumeric ID or a
// Powerpack UUID — used when Message.From is empty.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithDefaultCountry sets the calling code assumed for numbers written in
// national form with a leading 0.
func WithDefaultCountry(code string) Option { return func(p *Provider) { p.country = code } }

// WithCallback sets the URL Plivo posts delivery status updates to.
func WithCallback(u string) Option { return func(p *Provider) { p.callback = u } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Plivo provider for an auth ID and auth token.
func New(authID, authToken string, opts ...Option) *Provider {
	p := &Provider{authID: strings.TrimSpace(authID), authToken: authToken, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelSMS }

type request struct {
	Src           string `json:"src,omitempty"`
	PowerpackUUID string `json:"powerpack_uuid,omitempty"`
	Dst           string `json:"dst"`
	Text          string `json:"text"`
	URL           string `json:"url,omitempty"`
	Method        string `json:"method,omitempty"`
}

type reply struct {
	MessageUUID   []string `json:"message_uuid"`
	InvalidNumber []string `json:"invalid_number"`
	Error         string   `json:"error"`
}

// Send delivers msg in batches of up to 1000 recipients.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if _, err := notijet.CheckSMS(Name, msg, false); err != nil {
		return nil, err
	}
	if p.authID == "" || p.authToken == "" {
		return nil, notijet.Invalid(Name, "auth ID and token are required")
	}
	to, err := smsutil.Recipients(Name, msg, func(s string) (string, error) { return phone.International(s, p.country) })
	if err != nil {
		return nil, err
	}
	sender, err := smsutil.Sender(Name, msg, p.sender, true)
	if err != nil {
		return nil, err
	}

	endpoint := p.baseURL + "/Account/" + url.PathEscape(p.authID) + "/Message/"
	return notijet.SendBatches(ctx, Name, to, maxDestinations, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		body := request{Dst: strings.Join(batch, "<"), Text: msg.Text}
		if isUUID(sender) {
			body.PowerpackUUID = sender
		} else {
			body.Src = sender
		}
		if p.callback != "" {
			body.URL, body.Method = p.callback, http.MethodPost
		}
		req, err := httpc.JSON(http.MethodPost, endpoint, body)
		if err != nil {
			return nil, notijet.Fault(Name, "encoding request failed", err)
		}
		resp, err := p.client.Do(ctx, req.BasicAuth(p.authID, p.authToken))
		if err != nil {
			return nil, err
		}
		var r reply
		if err := p.client.Decode(resp, &r); err != nil {
			if !resp.OK() {
				return nil, mapError(resp.Status, resp.Snippet())
			}
			return nil, err
		}
		if !resp.OK() {
			return nil, mapError(resp.Status, r.Error)
		}
		if len(r.MessageUUID) == 0 {
			if len(r.InvalidNumber) > 0 {
				return nil, notijet.Rejected(Name, strconv.Itoa(resp.Status), "invalid numbers: "+strings.Join(r.InvalidNumber, ", "), notijet.ErrInvalidRecipient)
			}
			return nil, p.client.Unexpected(resp, "")
		}
		// UUIDs follow dst order with invalid numbers left out.
		var receipts []notijet.Receipt
		ids := r.MessageUUID
		for _, n := range batch {
			rc := notijet.Receipt{To: "+" + n}
			switch {
			case contains(r.InvalidNumber, n):
				rc.Status = "invalid"
			case len(ids) > 0:
				rc.MessageID, ids = ids[0], ids[1:]
				rc.Status = "queued"
			}
			receipts = append(receipts, rc)
		}
		return receipts, nil
	})
}

func contains(list []string, n string) bool {
	for _, s := range list {
		if strings.TrimPrefix(s, "+") == n {
			return true
		}
	}
	return false
}

// isUUID reports whether s has the 8-4-4-4-12 shape of a Powerpack UUID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

// mapError turns a Plivo error reply into a notijet error. Plivo reports
// errors as text, so the sentinel is chosen from the status and wording.
func mapError(status int, message string) error {
	code := strconv.Itoa(status)
	lower := strings.ToLower(message)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case strings.Contains(lower, "insufficient") || strings.Contains(lower, "credit") || strings.Contains(lower, "balance"):
		return notijet.Rejected(Name, code, message, notijet.ErrInsufficientCredit)
	case strings.Contains(lower, "src") || strings.Contains(lower, "source") || strings.Contains(lower, "powerpack"):
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
	case strings.Contains(lower, "dst") || strings.Contains(lower, "destination"):
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
	}
	return notijet.Rejected(Name, code, message, nil)
}
