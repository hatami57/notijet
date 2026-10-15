// Package infobip is a notijet SMS provider for Infobip
// (https://www.infobip.com/docs/api/channels/sms), using the
// /sms/2/text/advanced endpoint.
//
// Every Infobip account has its own API base URL (xxxxx.api.infobip.com),
// shown in the web interface:
//
//	p := infobip.New("https://xxxxx.api.infobip.com", apiKey, infobip.WithSender("Acme"))
//	p.Send(ctx, &notijet.Message{To: []string{"+447700900123"}, Text: "Your code is 1234"})
//
// Recipients go in bulk, up to 1000 per request. When Infobip rejects only
// some destinations, the send succeeds and their receipts carry the rejection
// status name; when it rejects all of them, Send fails.
package infobip

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/httpc"
	"github.com/hatami57/notijet/internal/smsutil"
	"github.com/hatami57/notijet/phone"
)

// Name is the provider name.
const Name = "infobip"

// maxDestinations is the most destinations sent per request.
const maxDestinations = 1000

// rejectedGroup is Infobip's status group for messages it will not deliver.
const rejectedGroup = 5

// Provider sends SMS through Infobip.
type Provider struct {
	baseURL   string
	apiKey    string
	sender    string
	country   string
	notifyURL string
	client    *httpc.Client
	hc        *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithSender sets the default sender (a number or an alphanumeric ID), used
// when Message.From is empty.
func WithSender(sender string) Option { return func(p *Provider) { p.sender = sender } }

// WithDefaultCountry sets the calling code assumed for numbers written in
// national form with a leading 0.
func WithDefaultCountry(code string) Option { return func(p *Provider) { p.country = code } }

// WithNotifyURL sets the URL Infobip posts delivery reports to.
func WithNotifyURL(u string) Option { return func(p *Provider) { p.notifyURL = u } }

// WithHTTPClient sets the HTTP client (default notijet.DefaultHTTPClient()).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// New returns an Infobip provider for the account's base URL and API key.
func New(baseURL, apiKey string, opts ...Option) *Provider {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL != "" && !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	p := &Provider{baseURL: baseURL, apiKey: strings.TrimSpace(apiKey)}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelSMS }

type request struct {
	Messages []message `json:"messages"`
}

type message struct {
	From         string        `json:"from,omitempty"`
	Destinations []destination `json:"destinations"`
	Text         string        `json:"text"`
	NotifyURL    string        `json:"notifyUrl,omitempty"`
	CallbackData string        `json:"callbackData,omitempty"`
}

type destination struct {
	To string `json:"to"`
}

type status struct {
	GroupID     int    `json:"groupId"`
	GroupName   string `json:"groupName"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type reply struct {
	Messages []struct {
		To        string `json:"to"`
		MessageID string `json:"messageId"`
		Status    status `json:"status"`
	} `json:"messages"`
	RequestError struct {
		ServiceException struct {
			MessageID string `json:"messageId"`
			Text      string `json:"text"`
		} `json:"serviceException"`
	} `json:"requestError"`
}

// Send delivers msg in batches of up to 1000 recipients. Message.ID is sent
// as callbackData, which Infobip returns in delivery reports.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if _, err := notijet.CheckSMS(Name, msg, false); err != nil {
		return nil, err
	}
	if p.baseURL == "" || p.apiKey == "" {
		return nil, notijet.Invalid(Name, "base URL and API key are required")
	}
	to, err := smsutil.Recipients(Name, msg, func(s string) (string, error) { return phone.International(s, p.country) })
	if err != nil {
		return nil, err
	}
	sender, _ := smsutil.Sender(Name, msg, p.sender, false)

	return notijet.SendBatches(ctx, Name, to, maxDestinations, func(ctx context.Context, batch []string) ([]notijet.Receipt, error) {
		m := message{From: sender, Text: msg.Text, NotifyURL: p.notifyURL, CallbackData: msg.ID}
		for _, n := range batch {
			m.Destinations = append(m.Destinations, destination{To: n})
		}
		req, err := httpc.JSON(http.MethodPost, p.baseURL+"/sms/2/text/advanced", request{Messages: []message{m}})
		if err != nil {
			return nil, notijet.Fault(Name, "encoding request failed", err)
		}
		resp, err := p.client.Do(ctx, req.Set("Authorization", "App "+p.apiKey))
		if err != nil {
			return nil, err
		}
		var r reply
		if err := p.client.Decode(resp, &r); err != nil {
			if resp.Status == http.StatusUnauthorized {
				return nil, notijet.Rejected(Name, "UNAUTHORIZED", "authentication failed", notijet.ErrUnauthorized)
			}
			return nil, err
		}
		if !resp.OK() {
			se := r.RequestError.ServiceException
			return nil, mapRequestError(resp.Status, se.MessageID, se.Text)
		}
		if len(r.Messages) == 0 {
			return nil, p.client.Unexpected(resp, "")
		}
		receipts := make([]notijet.Receipt, len(r.Messages))
		rejected := 0
		for i, m := range r.Messages {
			receipts[i] = notijet.Receipt{To: "+" + m.To, MessageID: m.MessageID, Status: m.Status.Name}
			if m.Status.GroupID == rejectedGroup {
				rejected++
			}
		}
		if rejected == len(r.Messages) {
			s := r.Messages[0].Status
			return nil, mapRejection(s.Name, s.Description)
		}
		return receipts, nil
	})
}

// mapRequestError turns a request-level serviceException into a notijet error.
func mapRequestError(httpStatus int, id, text string) error {
	if id == "" {
		id = strconv.Itoa(httpStatus)
	}
	if httpStatus == http.StatusUnauthorized || httpStatus == http.StatusForbidden || id == "UNAUTHORIZED" {
		return notijet.Rejected(Name, id, text, notijet.ErrUnauthorized)
	}
	return notijet.Rejected(Name, id, text, nil)
}

// mapRejection turns a REJECTED_* message status into a notijet error.
func mapRejection(name, description string) error {
	switch {
	case strings.Contains(name, "CREDIT"):
		return notijet.Rejected(Name, name, description, notijet.ErrInsufficientCredit)
	case strings.Contains(name, "SENDER"):
		return notijet.Rejected(Name, name, description, notijet.ErrInvalidSender)
	case strings.Contains(name, "DESTINATION"), strings.Contains(name, "PREFIX"), strings.Contains(name, "NETWORK"):
		return notijet.Rejected(Name, name, description, notijet.ErrInvalidRecipient)
	case strings.Contains(name, "FLOODING"):
		return notijet.Throttled(Name, name, description)
	}
	return notijet.Rejected(Name, name, description, nil)
}
