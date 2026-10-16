package aws

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildOne(t *testing.T, name string, kv ...string) (notijet.Provider, error) {
	t.Helper()
	settings := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		settings[kv[i]] = kv[i+1]
	}
	b := &config.Builder{Registry: Register(config.NewRegistry())}
	built, err := b.Build(context.Background(), []config.Spec{{Name: name, Settings: settings}})
	if err != nil {
		return nil, err
	}
	return built.Providers[0].Provider, nil
}

func TestAWSProviders(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(body))
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/v2/email") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"MessageId":"ses-1"}`)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, `<PublishResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/"><PublishResult><MessageId>sns-1</MessageId></PublishResult><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></PublishResponse>`)
	}))
	defer srv.Close()
	creds := []string{"region", "eu-west-1", "access_key_id", "AKIDTEST", "secret_access_key", "s3cr3t", "endpoint", srv.URL}

	p, err := buildOne(t, "sns", append([]string{"type", "sns", "sender", "Acme", "sms_type", "Promotional", "timeout", "5s"}, creds...)...)
	require.NoError(t, err)
	res, err := p.Send(context.Background(), &notijet.Message{To: []string{"+447700900123"}, Text: "hi"})
	require.NoError(t, err)
	assert.Equal(t, "sns-1", res.MessageID())

	p, err = buildOne(t, "ses", append([]string{"type", "ses", "from", "a@acme.test", "configuration_set", "events"}, creds...)...)
	require.NoError(t, err)
	res, err = p.Send(context.Background(), &notijet.Message{To: []string{"a@x.io"}, Subject: "s", Text: "t"})
	require.NoError(t, err)
	assert.Equal(t, "ses-1", res.MessageID())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 2)
	assert.Contains(t, seen[0], "Credential=AKIDTEST/", "static credentials sign the request")
	assert.Contains(t, seen[0], "AWS.SNS.SMS.SenderID")
	assert.Contains(t, seen[1], `"ConfigurationSetName":"events"`)

	_, err = buildOne(t, "sns", "type", "sns", "access_key_id", "x")
	assert.ErrorContains(t, err, "go together")
	_, err = buildOne(t, "sns", "type", "sns", "sms_type", "urgent")
	assert.Error(t, err)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("AWS_PROFILE", "no-such-profile")
	_, err = buildOne(t, "ses", "type", "ses")
	assert.ErrorContains(t, err, "loading AWS configuration")
	_, err = buildOne(t, "sns", "type", "sns")
	assert.ErrorContains(t, err, "loading AWS configuration")
}
