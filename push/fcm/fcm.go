// Package fcm is a notijet push provider for Firebase Cloud Messaging, HTTP
// v1 API (https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages/send).
//
// FCM authenticates with OAuth2 (scope
// https://www.googleapis.com/auth/firebase.messaging), usually as a service
// account. Pass an HTTP client that adds the token — for example
// google.DefaultClient from golang.org/x/oauth2/google — or a function that
// returns a fresh access token:
//
//	hc, _ := google.DefaultClient(ctx, "https://www.googleapis.com/auth/firebase.messaging")
//	p := fcm.New("my-project", fcm.WithHTTPClient(hc))
//	p.Send(ctx, &notijet.Message{
//	    To:      []string{deviceToken, "topic:news"},
//	    Subject: "Order shipped",          // notification title
//	    Text:    "Arriving tomorrow",      // notification body
//	    Data:    map[string]string{"order": "42"},
//	})
//
// Each recipient is a device registration token, "topic:<name>" or
// "condition:<expression>", and gets its own request. A message may be
// data-only (no Subject or Text). A token FCM reports as unregistered or
// invalid does not fail the send: its receipt carries an "invalid: <code>"
// status, so stale tokens can be pruned, and Send fails only when every
// recipient is invalid.
package fcm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/internal/httpc"
)

// Name is the provider name.
const Name = "fcm"

// DefaultBaseURL is the FCM API root.
const DefaultBaseURL = "https://fcm.googleapis.com"

// Provider sends push notifications through FCM.
type Provider struct {
	project string
	token   func(ctx context.Context) (string, error)
	baseURL string
	client  *httpc.Client
	hc      *http.Client
}

// Option configures a Provider.
type Option func(*Provider)

// WithHTTPClient sets the HTTP client. Without WithToken it must add the
// OAuth2 token itself (an oauth2.Client does).
func WithHTTPClient(hc *http.Client) Option { return func(p *Provider) { p.hc = hc } }

// WithToken sets a function returning a fresh OAuth2 access token, sent as a
// Bearer token on every request — plug in an oauth2.TokenSource there.
func WithToken(token func(ctx context.Context) (string, error)) Option {
	return func(p *Provider) { p.token = token }
}

// WithBaseURL overrides the API root, for tests or a proxy.
func WithBaseURL(u string) Option {
	return func(p *Provider) { p.baseURL = strings.TrimRight(u, "/") }
}

// New returns an FCM provider for a Firebase project ID. It needs
// WithHTTPClient (an OAuth2-authenticated client) or WithToken.
func New(projectID string, opts ...Option) *Provider {
	p := &Provider{project: strings.TrimSpace(projectID), baseURL: DefaultBaseURL}
	for _, opt := range opts {
		opt(p)
	}
	p.client = httpc.New(Name, p.hc)
	return p
}

func (p *Provider) Name() string             { return Name }
func (p *Provider) Channel() notijet.Channel { return notijet.ChannelPush }

type notification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type message struct {
	Token        string            `json:"token,omitempty"`
	Topic        string            `json:"topic,omitempty"`
	Condition    string            `json:"condition,omitempty"`
	Notification *notification     `json:"notification,omitempty"`
	Data         map[string]string `json:"data,omitempty"`
}

// Send delivers msg; see the package documentation.
func (p *Provider) Send(ctx context.Context, msg *notijet.Message) (*notijet.Result, error) {
	if err := check(msg); err != nil {
		return nil, err
	}
	if p.project == "" {
		return nil, notijet.Invalid(Name, "Firebase project ID is empty")
	}
	if p.hc == nil && p.token == nil {
		return nil, notijet.Invalid(Name, "no credentials: set WithHTTPClient (OAuth2 client) or WithToken")
	}
	var note *notification
	if msg.Subject != "" || msg.Text != "" {
		note = &notification{Title: msg.Subject, Body: msg.Text}
	}

	res := &notijet.Result{Provider: Name}
	sent := 0
	var firstInvalid error
	for _, to := range msg.Recipients() {
		if sent > 0 && ctx.Err() != nil {
			return nil, &notijet.PartialError{Result: res, Err: notijet.Fault(Name, "context done", ctx.Err())}
		}
		m := message{Notification: note, Data: msg.Data}
		switch {
		case strings.HasPrefix(to, "topic:"):
			m.Topic = strings.TrimPrefix(to, "topic:")
		case strings.HasPrefix(to, "/topics/"):
			m.Topic = strings.TrimPrefix(to, "/topics/")
		case strings.HasPrefix(to, "condition:"):
			m.Condition = strings.TrimPrefix(to, "condition:")
		default:
			m.Token = to
		}
		id, err := p.send(ctx, m)
		switch {
		case err == nil:
			sent++
			res.Receipts = append(res.Receipts, notijet.Receipt{To: to, MessageID: id, Status: "sent"})
		case errors.Is(err, notijet.ErrInvalidRecipient):
			if firstInvalid == nil {
				firstInvalid = err
			}
			res.Receipts = append(res.Receipts, notijet.Receipt{To: to, Status: "invalid: " + notijet.ProviderCode(err)})
		case sent > 0:
			return nil, &notijet.PartialError{Result: res, Err: err}
		default:
			return nil, err
		}
	}
	if sent == 0 {
		return nil, firstInvalid
	}
	return res, nil
}

// check validates msg for FCM: recipients, something to show or deliver
// (title, body or data), and no feature push cannot carry.
func check(msg *notijet.Message) error {
	switch {
	case msg == nil:
		return notijet.Invalid(Name, "message is nil")
	case len(msg.Recipients()) == 0:
		return notijet.Invalid(Name, "message has no recipient")
	case len(msg.Attachments) > 0:
		return notijet.Unsupported(Name, "Attachments")
	case len(notijet.CleanList(msg.CC))+len(notijet.CleanList(msg.BCC)) > 0:
		return notijet.Unsupported(Name, "CC/BCC")
	case strings.TrimSpace(msg.Text) == "" && strings.TrimSpace(msg.HTML) != "":
		return notijet.Unsupported(Name, "HTML body (set Text)")
	case strings.TrimSpace(msg.Text) == "" && msg.Template != nil:
		return notijet.Unsupported(Name, "Template")
	case strings.TrimSpace(msg.Subject) == "" && strings.TrimSpace(msg.Text) == "" && len(msg.Data) == 0:
		return notijet.Invalid(Name, "push message has no title (Subject), body (Text) or Data")
	}
	return nil
}

func (p *Provider) send(ctx context.Context, m message) (string, error) {
	endpoint := p.baseURL + "/v1/projects/" + url.PathEscape(p.project) + "/messages:send"
	r, err := httpc.JSON(http.MethodPost, endpoint, map[string]any{"message": m})
	if err != nil {
		return "", notijet.Fault(Name, "encoding request failed", err)
	}
	if p.token != nil {
		tok, err := p.token(ctx)
		if err != nil {
			return "", notijet.Fault(Name, "fetching OAuth2 token failed", err)
		}
		r.Set("Authorization", "Bearer "+tok)
	}
	resp, err := p.client.Do(ctx, r)
	if err != nil {
		return "", err
	}
	if !resp.OK() {
		return "", mapError(resp.Status, resp.Body)
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := p.client.Decode(resp, &out); err != nil {
		return "", err
	}
	if out.Name == "" {
		return "", p.client.Unexpected(resp, "")
	}
	return out.Name, nil
}

type errorReply struct {
	Error struct {
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

// mapError turns an FCM error reply into a notijet error. The code is the
// FCM errorCode when there is one, else the Google status, else the HTTP
// status.
func mapError(status int, body []byte) error {
	var r errorReply
	_ = json.Unmarshal(body, &r)
	message := r.Error.Message
	if message == "" {
		message = httpc.Snippet(body)
	}
	code := r.Error.Status
	for _, d := range r.Error.Details {
		if d.ErrorCode != "" {
			code = d.ErrorCode
			break
		}
	}
	if code == "" {
		code = strconv.Itoa(status)
	}
	lower := strings.ToLower(message)
	switch {
	case code == "UNREGISTERED" || code == "NOT_FOUND":
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
	case code == "INVALID_ARGUMENT" && (strings.Contains(lower, "registration token") || strings.Contains(lower, "topic")):
		return notijet.Rejected(Name, code, message, notijet.ErrInvalidRecipient)
	case code == "SENDER_ID_MISMATCH" || code == "THIRD_PARTY_AUTH_ERROR" ||
		status == http.StatusUnauthorized || status == http.StatusForbidden:
		return notijet.Rejected(Name, code, message, notijet.ErrUnauthorized)
	case code == "QUOTA_EXCEEDED":
		return notijet.Throttled(Name, code, message)
	}
	return notijet.Rejected(Name, code, message, nil)
}
