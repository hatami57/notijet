// Package ghasedak is a notijet SMS provider for Ghasedak
// (https://ghasedak.me), gateway REST API v1.
//
// A plain message goes through SendSingleSMS, one request per recipient; a
// message with a Template goes through SendOtpSMS (up to 100 receptors per
// request), with Template.ID as the template name and the params as its named
// inputs:
//
//	p := ghasedak.New(apiKey, ghasedak.WithSender("30005088"))
//	p.Send(ctx, &notijet.Message{
//	    To:       []string{"09121234567"},
//	    Template: notijet.NewTemplate("verify", "Code", "52341"),
//	})
//
// Without a sender, Ghasedak uses the account's highest-priority line.
// Message.ID is forwarded as clientReferenceId (suffixed with the recipient's
// index when there are several). Set Message.Data["voice"] = "true" to deliver
// a template as a voice call.
package ghasedak

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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
const Name = "ghasedak"

// DefaultBaseURL is Ghasedak's web service root.
const DefaultBaseURL = "https://gateway.ghasedak.me/rest/api/v1/WebService"

// maxReceptors is the most receptors sent per SendOtpSMS request.
const maxReceptors = 100

// Provider sends SMS through Ghasedak.
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
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns a Ghasedak provider for apiKey.
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

type singleRequest struct {
	LineNumber        string `json:"lineNumber,omitempty"`
	Receptor          string `json:"receptor"`
	Message           string `json:"message"`
	ClientReferenceID string `json:"clientReferenceId,omitempty"`
}

type otpRequest struct {
	Receptors    []otpReceptor `json:"receptors"`
	TemplateName string        `json:"templateName"`
	Inputs       []otpInput    `json:"inputs"`
	IsVoice      bool          `json:"isVoice"`
}

type otpReceptor struct {
	Mobile            string `json:"mobile"`
	ClientReferenceID string `json:"clientReferenceId"`
}

type otpInput struct {
	Param string `json:"param"`
	Value string `json:"value"`
}

type item struct {
	Receptor  string          `json:"receptor"`
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
	refs := references(msg.ID, to)

	if useTemplate {
		inputs := make([]otpInput, len(msg.Template.Params))
		for i, prm := range msg.Template.Params {
			inputs[i] = otpInput{Param: prm.Name, Value: prm.Value}
		}
		offset := 0
		return notijet.SendBatches(ctx, Name, to, maxReceptors, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
			req := otpRequest{TemplateName: msg.Template.ID, Inputs: inputs, IsVoice: smsutil.Flag(msg, "voice")}
			for i, m := range batch {
				req.Receptors = append(req.Receptors, otpReceptor{Mobile: m, ClientReferenceID: refs[offset+i]})
			}
			offset += len(batch)
			var data struct {
				Items []item `json:"items"`
			}
			if err := p.post(ctx, "/SendOtpSMS", req, &data); err != nil {
				return nil, err
			}
			receipts := make([]notijet.Receipt, len(data.Items))
			for i, it := range data.Items {
				receipts[i] = notijet.Receipt{To: it.Receptor, MessageID: string(it.MessageID), Cost: string(it.Cost)}
			}
			return receipts, nil
		})
	}

	sender, _ := smsutil.Sender(Name, msg, p.sender, false)
	i := 0
	return notijet.SendBatches(ctx, Name, to, 1, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		req := singleRequest{LineNumber: sender, Receptor: batch[0], Message: msg.Text, ClientReferenceID: refs[i]}
		i++
		var data item
		if err := p.post(ctx, "/SendSingleSMS", req, &data); err != nil {
			return nil, err
		}
		return []notijet.Receipt{{To: batch[0], MessageID: string(data.MessageID), Cost: string(data.Cost)}}, nil
	})
}

// references returns one clientReferenceId per recipient: id itself for a
// single recipient, id-1, id-2 ... for several, and random ones when id is
// empty (SendOtpSMS requires a reference per receptor).
func references(id string, to []string) []string {
	refs := make([]string, len(to))
	for i := range to {
		switch {
		case id == "":
			refs[i] = rand.Text()
		case len(to) == 1:
			refs[i] = id
		default:
			refs[i] = id + "-" + strconv.Itoa(i+1)
		}
	}
	return refs
}

type envelope struct {
	IsSuccess  bool            `json:"isSuccess"`
	StatusCode int             `json:"statusCode"`
	Message    string          `json:"message"`
	Data       json.RawMessage `json:"data"`
}

func (p *Provider) post(ctx context.Context, path string, body, out any) error {
	req, err := httpc.JSON(http.MethodPost, p.baseURL+path, body)
	if err != nil {
		return notijet.Fault(Name, "encoding request failed", err)
	}
	req.Set("ApiKey", p.apiKey)
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		// Ghasedak reuses 429 for "template parameter not found"; trust the
		// body over the transport-level guess when it says so.
		var env envelope
		if resp != nil && errors.Is(err, notijet.ErrRateLimited) &&
			json.Unmarshal(resp.Body, &env) == nil && env.StatusCode != 0 && !env.IsSuccess {
			return mapError(env.StatusCode, env.Message)
		}
		return err
	}
	var env envelope
	if err := p.client.Decode(resp, &env); err != nil {
		return err
	}
	if !env.IsSuccess {
		if env.StatusCode == 0 {
			return p.client.Unexpected(resp, "")
		}
		return mapError(env.StatusCode, env.Message)
	}
	if len(env.Data) == 0 || json.Unmarshal(env.Data, out) != nil {
		return p.client.Unexpected(resp, "")
	}
	return nil
}

// mapError turns a Ghasedak statusCode into a notijet error.
func mapError(status int, message string) error {
	code := strconv.Itoa(status)
	switch status {
	case 401, 416:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case 406, 412:
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidSender)
	case 418:
		return notijet.Rejected(Name, code, message, notijet.ErrInsufficientCredit)
	case 428, 429:
		return notijet.Rejected(Name, code, message, notijet.ErrTemplate)
	case 500:
		return notijet.FaultCode(Name, code, message, nil)
	}
	return notijet.Rejected(Name, code, message, nil)
}
