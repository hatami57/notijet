// Package smsir is a notijet SMS provider for SMS.ir (https://sms.ir), REST
// API v1.
//
// A plain message goes through /v1/send/bulk (up to 100 mobiles per request)
// from a numeric sender line; a message with a Template goes through
// /v1/send/verify, one request per recipient, with Template.ID as the numeric
// template ID and the params as named template parameters:
//
//	p := smsir.New(apiKey, smsir.WithSender("30007732000000"))
//	p.Send(ctx, &notijet.Message{
//	    To:       []string{"09121234567"},
//	    Template: notijet.NewTemplate("123456", "CODE", "52341"),
//	})
package smsir

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
const Name = "smsir"

// DefaultBaseURL is SMS.ir's API root.
const DefaultBaseURL = "https://api.sms.ir"

// maxMobiles is the most mobiles /v1/send/bulk accepts in one request.
const maxMobiles = 100

// Provider sends SMS through SMS.ir.
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
// Bulk sends need one; verify sends use the account's verify line.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns an SMS.ir provider for apiKey.
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

type bulkRequest struct {
	LineNumber  int64    `json:"lineNumber"`
	MessageText string   `json:"messageText"`
	Mobiles     []string `json:"mobiles"`
}

type verifyRequest struct {
	Mobile     string        `json:"mobile"`
	TemplateID int64         `json:"templateId"`
	Parameters []verifyParam `json:"parameters"`
}

type verifyParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type envelope struct {
	Status  int             `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type bulkData struct {
	PackID     string            `json:"packId"`
	MessageIDs []flexjson.String `json:"messageIds"`
	Cost       flexjson.String   `json:"cost"`
}

type verifyData struct {
	MessageID flexjson.String `json:"messageId"`
	Cost      flexjson.String `json:"cost"`
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
	to, err := smsutil.Recipients(Name, msg, phone.IranMobile)
	if err != nil {
		return nil, err
	}

	if useTemplate {
		id, err := strconv.ParseInt(strings.TrimSpace(msg.Template.ID), 10, 64)
		if err != nil {
			return nil, notijet.InvalidTemplate(Name, "Template.ID must be the numeric SMS.ir template ID")
		}
		params := make([]verifyParam, len(msg.Template.Params))
		for i, prm := range msg.Template.Params {
			params[i] = verifyParam{Name: prm.Name, Value: prm.Value}
		}
		return notijet.SendBatches(ctx, Name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
			var data verifyData
			if err := p.post(ctx, "/v1/send/verify", verifyRequest{Mobile: batch[0], TemplateID: id, Parameters: params}, &data); err != nil {
				return nil, err
			}
			return []notijet.Receipt{{To: batch[0], MessageID: string(data.MessageID), Cost: string(data.Cost)}}, nil
		})
	}

	sender, err := smsutil.Sender(Name, msg, p.sender, true)
	if err != nil {
		return nil, err
	}
	line, err := strconv.ParseInt(sender, 10, 64)
	if err != nil {
		return nil, notijet.InvalidSender(Name, "sender line must be numeric")
	}
	return notijet.SendBatches(ctx, Name, to, maxMobiles, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		var data bulkData
		if err := p.post(ctx, "/v1/send/bulk", bulkRequest{LineNumber: line, MessageText: msg.Text, Mobiles: batch}, &data); err != nil {
			return nil, err
		}
		receipts := make([]notijet.Receipt, len(batch))
		for i, m := range batch {
			receipts[i] = notijet.Receipt{To: m}
			if i < len(data.MessageIDs) {
				receipts[i].MessageID = string(data.MessageIDs[i])
			}
		}
		if len(batch) == 1 {
			receipts[0].Cost = string(data.Cost)
		}
		return receipts, nil
	})
}

func (p *Provider) post(ctx context.Context, path string, body, out any) error {
	req, err := httpc.JSON(http.MethodPost, p.baseURL+path, body)
	if err != nil {
		return notijet.Fault(Name, "encoding request failed", err)
	}
	req.Set("X-API-KEY", p.apiKey)
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return err
	}
	var env envelope
	if err := p.client.Decode(resp, &env); err != nil {
		return err
	}
	if env.Status != 1 {
		if env.Status == 0 && !resp.OK() && env.Message == "" {
			return p.client.Unexpected(resp, "")
		}
		return mapError(env.Status, env.Message)
	}
	if !resp.OK() || len(env.Data) == 0 || json.Unmarshal(env.Data, out) != nil {
		return p.client.Unexpected(resp, "")
	}
	return nil
}

// mapError turns an SMS.ir status into a notijet error.
func mapError(status int, message string) error {
	code := strconv.Itoa(status)
	switch status {
	case 10, 11, 12, 13, 14:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case 20:
		return notijet.Throttled(Name, code, message)
	case 101, 123:
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
	case 102:
		return notijet.Rejected(Name, code, message, notijet.ErrInsufficientCredit)
	case 104, 115:
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
	case 113, 114, 116, 119:
		return notijet.Rejected(Name, code, message, notijet.ErrTemplate)
	case 0:
		return notijet.FaultCode(Name, code, message, nil)
	}
	return notijet.Rejected(Name, code, message, nil)
}
