package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func server(t *testing.T, status int, reply string) (*Provider, *[]map[string]any) {
	t.Helper()
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/services/T0/B0/s3cr3t", r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(b, &body))
		calls = append(calls, body)
		w.WriteHeader(status)
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL + "/services/T0/B0/s3cr3t"), &calls
}

func TestSend(t *testing.T) {
	p, calls := server(t, 200, "ok")
	res, err := p.Send(context.Background(), &notijet.Message{Subject: "Deploy", Text: "*api* is live"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"text": "Deploy\n\n*api* is live"}, (*calls)[0])
	assert.Equal(t, "slack", res.Provider)
	assert.Equal(t, "ok", res.Receipts[0].Status)

	blocks := `[{"type":"section","text":{"type":"mrkdwn","text":"hi"}}]`
	_, err = p.Send(context.Background(), &notijet.Message{Text: "hi", Data: map[string]string{"blocks": blocks}})
	require.NoError(t, err)
	assert.Len(t, (*calls)[1]["blocks"], 1)
}

func TestValidation(t *testing.T) {
	p, calls := server(t, 200, "ok")
	cases := map[string]*notijet.Message{
		"recipients": {To: []string{"#general"}, Text: "x"},
		"html only":  {HTML: "<b>x</b>"},
		"bad blocks": {Text: "x", Data: map[string]string{"blocks": `{"type":"section"}`}},
		"bad json":   {Text: "x", Data: map[string]string{"blocks": `[`}},
		"no body":    {},
		"bcc":        {Text: "x", BCC: []string{"a@x.io"}},
	}
	for name, msg := range cases {
		_, err := p.Send(context.Background(), msg)
		assert.Error(t, err, name)
		assert.False(t, notijet.IsRetryable(err), name)
	}
	_, err := p.Send(context.Background(), &notijet.Message{To: []string{"#x"}, Text: "x"})
	assert.ErrorIs(t, err, notijet.ErrUnsupported)
	_, err = New("").Send(context.Background(), &notijet.Message{Text: "x"})
	assert.Error(t, err)
	assert.Empty(t, *calls)
}

func TestErrors(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		code     string
		sentinel error
	}{
		{403, "invalid_token", "invalid_token", notijet.ErrUnauthorized},
		{404, "no_service", "no_service", notijet.ErrUnauthorized},
		{404, "channel_not_found", "channel_not_found", notijet.ErrInvalidRecipient},
		{410, "channel_is_archived", "channel_is_archived", notijet.ErrInvalidRecipient},
		{400, "invalid_payload", "invalid_payload", nil},
		{403, "<html>forbidden</html>", "403", notijet.ErrUnauthorized},
		{400, "", "400", nil},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			p, _ := server(t, tc.status, tc.body)
			_, err := p.Send(context.Background(), &notijet.Message{Text: "x"})
			require.Error(t, err)
			if tc.sentinel != nil {
				assert.ErrorIs(t, err, tc.sentinel)
			}
			assert.Equal(t, tc.code, notijet.ProviderCode(err))
			assert.False(t, notijet.IsRetryable(err))
			assert.NotContains(t, err.Error(), "s3cr3t")
		})
	}
}

func TestConformance(t *testing.T) {
	providertest.RunHTTP(t, providertest.HTTPCase{
		Channel: notijet.ChannelChat,
		New: func(baseURL string, hc *http.Client) notijet.Provider {
			return New(baseURL+"/services/T0/B0/s3cr3t", WithHTTPClient(hc))
		},
		Message:           func() *notijet.Message { return &notijet.Message{Text: "hi"} },
		Secret:            "s3cr3t",
		RecipientOptional: true,
	})
}
