// Package ippanel is a notijet SMS provider for IPPanel
// (https://ippanel.com) and the panels built on it, such as FarazSMS, over
// REST API v1 (api2.ippanel.com).
//
// A plain message goes through /sms/send/webservice/single (up to 100
// recipients per request, one message ID for them all); a message with a
// Template goes through /sms/pattern/normal/send, one request per recipient,
// with Template.ID as the pattern code and the params as its variables:
//
//	p := ippanel.New(apiKey, ippanel.WithSender("+983000505"))
//	p.Send(ctx, &notijet.Message{
//	    To:       []string{"09121234567"},
//	    Template: notijet.NewTemplate("p8x7sd1ab", "code", "52341"),
//	})
//
// Recipients are sent in E.164 form; numbers without a country code are
// taken as Iranian.
package ippanel

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/flexjson"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/smsutil"
	"github.com/hatami57/notijet/phone"
)

// Name is the provider name.
const Name = "ippanel"

// DefaultBaseURL is IPPanel's REST API root.
const DefaultBaseURL = "https://api2.ippanel.com/api/v1"

// maxRecipients is the most recipients sent per webservice request.
const maxRecipients = 100

// Provider sends SMS through IPPanel.
type Provider struct {
	apiKey  string
	sender  string
	baseURL string
	client  *httpc.Client
	hc      *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithSender sets the default sender line, used when Message.From is empty.
// IPPanel requires a sender for every message.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a white-label panel.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns an IPPanel provider for apiKey.
func New(apiKey string, opts ...Option) *Provider {
	p := &Provider{apiKey: strings.TrimSpace(apiKey), baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelSMS }

type webserviceRequest struct {
	Sender      string            `json:"sender"`
	Recipient   []string          `json:"recipient"`
	Message     string            `json:"message"`
	Description map[string]string `json:"description"`
}

type patternRequest struct {
	Code      string            `json:"code"`
	Sender    string            `json:"sender"`
	Recipient string            `json:"recipient"`
	Variable  map[string]string `json:"variable"`
}

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	useTemplate, err := notijet.CheckSMS(Name, msg, true)
	if err != nil {
		return nil, err
	}
	if p.apiKey == "" {
		return nil, notijet.Invalid(Name, "API key is empty")
	}
	to, err := smsutil.Recipients(Name, msg, func(s string) (string, error) {
		return phone.E164(s, phone.IranCountryCode)
	})
	if err != nil {
		return nil, err
	}
	sender, err := smsutil.Sender(Name, msg, p.sender, true)
	if err != nil {
		return nil, err
	}

	if useTemplate {
		vars := msg.Template.Map()
		return notijet.SendBatches(ctx, Name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
			req := patternRequest{Code: msg.Template.ID, Sender: sender, Recipient: batch[0], Variable: vars}
			id, err := p.post(ctx, "/sms/pattern/normal/send", req, true)
			if err != nil {
				return nil, err
			}
			return []notijet.Receipt{{To: batch[0], MessageID: id}}, nil
		})
	}

	return notijet.SendBatches(ctx, Name, to, maxRecipients, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		req := webserviceRequest{
			Sender:    sender,
			Recipient: batch,
			Message:   msg.Text,
			Description: map[string]string{
				"summary":         summary(msg.Text),
				"count_recipient": strconv.Itoa(len(batch)),
			},
		}
		id, err := p.post(ctx, "/sms/send/webservice/single", req, false)
		if err != nil {
			return nil, err
		}
		if len(batch) == 1 {
			return []notijet.Receipt{{To: batch[0], MessageID: id}}, nil
		}
		return []notijet.Receipt{{MessageID: id}}, nil
	})
}

// summary is the short description IPPanel shows for a send in the panel.
func summary(text string) string {
	r := []rune(text)
	if len(r) > 30 {
		r = r[:30]
	}
	return string(r)
}

type envelope struct {
	Status       string          `json:"status"`
	Code         int             `json:"code"`
	ErrorMessage string          `json:"error_message"`
	Data         json.RawMessage `json:"data"`
}

func (p *Provider) post(ctx context.Context, path string, body any, pattern bool) (string, error) {
	req, err := httpc.JSON(http.MethodPost, p.baseURL+path, body)
	if err != nil {
		return "", notijet.Fault(Name, "encoding request failed", err)
	}
	req.Set("Apikey", p.apiKey)
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return "", err
	}
	var env envelope
	if err := p.client.Decode(resp, &env); err != nil {
		if !resp.OK() {
			return "", mapError(resp.Status, resp.Snippet(), pattern)
		}
		return "", err
	}
	if !resp.OK() || (env.Code != 0 && env.Code != 200 && env.Code != 201) {
		code := env.Code
		if code == 0 {
			code = resp.Status
		}
		return "", mapError(code, errorText(env), pattern)
	}
	var data struct {
		MessageID flexjson.String `json:"message_id"`
	}
	if json.Unmarshal(env.Data, &data) != nil || data.MessageID == "" {
		return "", p.client.Unexpected(resp, "")
	}
	return string(data.MessageID), nil
}

// errorText collects the error message: error_message, or the data.error
// string or field errors IPPanel sends on validation failures.
func errorText(env envelope) string {
	if env.ErrorMessage != "" {
		return env.ErrorMessage
	}
	var data struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(env.Data, &data) != nil || len(data.Error) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(data.Error, &s) == nil {
		return s
	}
	var fields map[string][]string
	if json.Unmarshal(data.Error, &fields) == nil {
		var parts []string
		for k, v := range fields {
			parts = append(parts, k+": "+strings.Join(v, ", "))
		}
		return strings.Join(parts, "; ")
	}
	return string(data.Error)
}

// mapError turns an IPPanel error code (the HTTP status it mirrors) into a
// notijet error.
func mapError(code int, message string, pattern bool) error {
	c := strconv.Itoa(code)
	lower := strings.ToLower(message)
	switch {
	case code == 401 || code == 403:
		return notijet.Rejected(Name, c, message, notijet.ErrUnauthorized)
	case strings.Contains(lower, "credit") || strings.Contains(message, "اعتبار"):
		return notijet.Rejected(Name, c, message, notijet.ErrInsufficientCredit)
	case pattern && (code == 404 || strings.Contains(lower, "pattern")):
		return notijet.Rejected(Name, c, message, notijet.ErrTemplate)
	}
	return notijet.Rejected(Name, c, message, nil)
}
