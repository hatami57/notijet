// Package melipayamak is a notijet SMS provider for Melipayamak
// (https://melipayamak.com) and other Payamak Panel resellers, over the
// rest.payamak-panel.com REST API.
//
// A plain message goes through SendSMS (up to 100 recipients per request); a
// message with a Template goes through BaseServiceNumber ("shared service
// line" pattern sending), one request per recipient, with Template.ID as the
// numeric bodyId and the param values, in order, as the pattern arguments:
//
//	p := melipayamak.New(username, password, melipayamak.WithSender("50004001234567"))
//	p.Send(ctx, &notijet.Message{
//	    To:       []string{"09121234567"},
//	    Template: notijet.NewTemplate("123456", "code", "52341"),
//	})
//
// Set Message.Data["flash"] = "true" to send a plain message as a flash SMS.
package melipayamak

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
const Name = "melipayamak"

// DefaultBaseURL is the Payamak Panel REST API root.
const DefaultBaseURL = "https://rest.payamak-panel.com/api/SendSMS"

// maxRecipients is the most recipients sent per SendSMS request.
const maxRecipients = 100

// Provider sends SMS through Melipayamak.
type Provider struct {
	username, password string
	sender             string
	baseURL            string
	client             *httpc.Client
	hc                 *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithSender sets the default sender line, used when Message.From is empty.
// Plain messages need one; pattern messages use the shared service line.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or another reseller's domain.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Melipayamak provider for the panel's web service credentials.
func New(username, password string, opts ...Option) *Provider {
	p := &Provider{username: strings.TrimSpace(username), password: password, baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelSMS }

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	useTemplate, err := notijet.CheckSMS(Name, msg, true)
	if err != nil {
		return nil, err
	}
	if p.username == "" || p.password == "" {
		return nil, notijet.Invalid(Name, "username and password are required")
	}
	to, err := smsutil.Recipients(Name, msg, phone.IranMobile)
	if err != nil {
		return nil, err
	}

	if useTemplate {
		bodyID := strings.TrimSpace(msg.Template.ID)
		if _, err := strconv.ParseInt(bodyID, 10, 64); err != nil {
			return nil, notijet.InvalidTemplate(Name, "Template.ID must be the numeric pattern bodyId")
		}
		text := strings.Join(msg.Template.Values(), ";")
		return notijet.SendBatches(ctx, Name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
			return p.call(ctx, "BaseServiceNumber", batch, url.Values{"to": {batch[0]}, "bodyId": {bodyID}, "text": {text}})
		})
	}

	sender, err := smsutil.Sender(Name, msg, p.sender, true)
	if err != nil {
		return nil, err
	}
	flash := strconv.FormatBool(smsutil.Flag(msg, "flash"))
	return notijet.SendBatches(ctx, Name, to, maxRecipients, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		form := url.Values{"to": {strings.Join(batch, ",")}, "from": {sender}, "text": {msg.Text}, "isFlash": {flash}}
		return p.call(ctx, "SendSMS", batch, form)
	})
}

type reply struct {
	Value        flexjson.String `json:"Value"`
	RetStatus    int             `json:"RetStatus"`
	StrRetStatus string          `json:"StrRetStatus"`
}

// Error codes occupy -10..35; a recId (message ID) is a larger number.
const minCode, maxCode = -10, 35

func (p *Provider) call(ctx context.Context, method string, batch []string, form url.Values) ([]notijet.Receipt, error) {
	form.Set("username", p.username)
	form.Set("password", p.password)
	resp, err := p.client.Do(ctx, httpc.Form(http.MethodPost, p.baseURL+"/"+method, form))
	if err != nil {
		return nil, err
	}
	var r reply
	if err := p.client.Decode(resp, &r); err != nil {
		return nil, err
	}

	// The reply carries one recId, or one per recipient joined by commas.
	parts := strings.Split(string(r.Value), ",")
	ids := make([]string, 0, len(parts))
	for _, s := range parts {
		s = strings.TrimSpace(s)
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, p.client.Unexpected(resp, "")
		}
		if v >= minCode && v <= maxCode {
			return nil, mapError(int(v), r.StrRetStatus)
		}
		ids = append(ids, s)
	}
	if r.RetStatus != 1 {
		return nil, mapError(r.RetStatus, r.StrRetStatus)
	}
	if !resp.OK() {
		return nil, p.client.Unexpected(resp, "")
	}
	if len(ids) == len(batch) {
		receipts := make([]notijet.Receipt, len(batch))
		for i := range batch {
			receipts[i] = notijet.Receipt{To: batch[i], MessageID: ids[i]}
		}
		return receipts, nil
	}
	return []notijet.Receipt{{MessageID: ids[0]}}, nil
}

// mapError turns a Melipayamak result code into a notijet error.
func mapError(code int, message string) error {
	c := strconv.Itoa(code)
	if message == "" {
		message = "provider rejected the message (code " + c + ")"
	}
	switch code {
	case 0, 10, -1:
		return notijet.Rejected(Name, c, message, notijet.ErrUnauthorized)
	case 2:
		return notijet.Rejected(Name, c, message, notijet.ErrInsufficientCredit)
	case 5, 9, -3, -7:
		return notijet.Rejected(Name, c, message, notijet.ErrInvalidSender)
	case 16, 18, 35:
		return notijet.Rejected(Name, c, message, notijet.ErrInvalidRecipient)
	case -4, -5:
		return notijet.Rejected(Name, c, message, notijet.ErrTemplate)
	case 6, -6:
		return notijet.FaultCode(Name, c, message, nil)
	}
	return notijet.Rejected(Name, c, message, nil)
}
