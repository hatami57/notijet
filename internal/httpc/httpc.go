// Package httpc is the small HTTP helper every HTTP-based provider shares. It
// sends a request, reads the whole reply, and turns transport failures, 429s
// and 5xx replies into notijet errors, leaving 2xx and 4xx bodies for the
// provider to interpret — most providers report their own error codes in a 4xx
// body.
package httpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hatami57/notijet"
)

// UserAgent is sent with every request.
const UserAgent = "notijet (+https://github.com/hatami57/notijet)"

// maxBody caps how much of a reply is read, so a misbehaving upstream cannot
// exhaust memory.
const maxBody = 4 << 20

// snippetLen caps how much of a reply body is quoted into an error.
const snippetLen = 512

// Client sends requests on behalf of one provider.
type Client struct {
	provider string
	http     *http.Client
}

// New returns a Client for provider. A nil hc means notijet.DefaultHTTPClient.
func New(provider string, hc *http.Client) *Client {
	if hc == nil {
		hc = notijet.DefaultHTTPClient()
	}
	return &Client{provider: provider, http: hc}
}

// Request is one HTTP request.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

// NewRequest builds a body-less request.
func NewRequest(method, rawURL string) *Request {
	return &Request{Method: method, URL: rawURL, Header: http.Header{}}
}

// JSON builds a request with v encoded as its JSON body.
func JSON(method, rawURL string, v any) (*Request, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	r := NewRequest(method, rawURL)
	r.Body = b
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}

// Form builds a request with values encoded as an
// application/x-www-form-urlencoded body.
func Form(method, rawURL string, values url.Values) *Request {
	r := NewRequest(method, rawURL)
	r.Body = []byte(values.Encode())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// Set sets a header and returns r for chaining.
func (r *Request) Set(key, value string) *Request {
	r.Header.Set(key, value)
	return r
}

// BasicAuth sets HTTP basic authentication and returns r for chaining.
func (r *Request) BasicAuth(user, password string) *Request {
	req := http.Request{Header: r.Header}
	req.SetBasicAuth(user, password)
	return r
}

// Response is a fully read reply.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// OK reports a 2xx status.
func (r *Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Snippet returns the start of the body, for error messages.
func (r *Response) Snippet() string { return Snippet(r.Body) }

// Snippet returns the start of body, trimmed, for error messages.
func Snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > snippetLen {
		s = s[:snippetLen] + "…"
	}
	return s
}

// Do sends r and reads the reply. A transport failure or an unreadable body is
// a notijet Fault; a 429 is Throttled; a 5xx is a Fault carrying the status and
// body. Every other reply, 4xx included, is returned for the provider to
// interpret.
func (c *Client) Do(ctx context.Context, r *Request) (*Response, error) {
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return nil, notijet.Fault(c.provider, "building request failed", err)
	}
	for k, v := range r.Header {
		req.Header[k] = v
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Strip the URL from the transport error: several providers put the API
		// key in the path or query string, and errors travel into logs.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, notijet.Fault(c.provider, "request failed", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, notijet.Fault(c.provider, "reading reply failed", err)
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return out, notijet.Throttled(c.provider, strconv.Itoa(resp.StatusCode), "")
	case resp.StatusCode >= 500:
		return out, notijet.FaultCode(c.provider, strconv.Itoa(resp.StatusCode),
			fmt.Sprintf("provider returned HTTP %d", resp.StatusCode),
			errors.New(out.Snippet()))
	}
	return out, nil
}

// Decode decodes the reply body as JSON into out. A body that does not decode
// is a Fault carrying the body, since it says nothing about the message.
func (c *Client) Decode(r *Response, out any) error {
	if err := json.Unmarshal(r.Body, out); err != nil {
		return notijet.FaultCode(c.provider, strconv.Itoa(r.Status), "unreadable provider reply",
			fmt.Errorf("%w: %s", err, r.Snippet()))
	}
	return nil
}

// Unexpected reports a reply the provider should not have sent (an
// undocumented status, a 2xx without the expected fields) as a Fault carrying
// the status and body.
func (c *Client) Unexpected(r *Response, message string) error {
	if message == "" {
		message = fmt.Sprintf("unexpected provider reply (HTTP %d)", r.Status)
	}
	return notijet.FaultCode(c.provider, strconv.Itoa(r.Status), message, errors.New(r.Snippet()))
}

// Provider returns the provider name the client reports errors under.
func (c *Client) Provider() string { return c.provider }
