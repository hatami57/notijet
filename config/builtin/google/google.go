// Package google registers the notijet providers that authenticate with
// Google OAuth2 — gmail (email through the Gmail API) and fcm (Firebase
// Cloud Messaging push) — in a config.Registry. They live apart from
// config/builtin so applications that do not use them do not link OAuth2:
//
//	r := google.Register(builtin.Registry())
//
// Settings per type (without credentials, Application Default Credentials;
// timeout bounds each request):
//
//	gmail  credentials_file or credentials_json (service account), user, from,
//	       base_url, timeout
//	fcm    project_id*, credentials_file or credentials_json, base_url, timeout
package google

import (
	"context"
	"net/http"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/config"
	"github.com/hatami57/notijet/config/builtin"
	"github.com/hatami57/notijet/email/gmail"
	"github.com/hatami57/notijet/push/fcm"
)

// OAuth2 scopes the providers request.
const (
	GmailScope = "https://www.googleapis.com/auth/gmail.send"
	FCMScope   = "https://www.googleapis.com/auth/firebase.messaging"
)

// Register adds the gmail and fcm types to r and returns it.
func Register(r *config.Registry) *config.Registry {
	return r.Register("gmail", Gmail).Register("fcm", FCM)
}

// Gmail builds a Gmail API email provider.
func Gmail(ctx context.Context, s *config.Settings) (notijet.Provider, error) {
	user := s.String("user")
	opts := []gmail.Option{gmail.WithFrom(s.String("from"))}
	if user != "" {
		opts = append(opts, gmail.WithUser(user))
	}
	if u := s.String("base_url"); u != "" {
		opts = append(opts, gmail.WithBaseURL(u))
	}
	hc, err := client(ctx, s, GmailScope, user)
	if err != nil {
		return nil, err
	}
	return gmail.New(append(opts, gmail.WithHTTPClient(hc))...), nil
}

// FCM builds a Firebase Cloud Messaging push provider.
func FCM(ctx context.Context, s *config.Settings) (notijet.Provider, error) {
	project := s.Required("project_id")
	var opts []fcm.Option
	if u := s.String("base_url"); u != "" {
		opts = append(opts, fcm.WithBaseURL(u))
	}
	hc, err := client(ctx, s, FCMScope, "")
	if err != nil {
		return nil, err
	}
	return fcm.New(project, append(opts, fcm.WithHTTPClient(hc))...), nil
}

// client returns an HTTP client authorized for scope: from a service
// account key (credentials_json or credentials_file), impersonating subject
// when set, or from Application Default Credentials.
func client(ctx context.Context, s *config.Settings, scope, subject string) (*http.Client, error) {
	data := []byte(s.String("credentials_json"))
	if f := s.String("credentials_file"); f != "" {
		if len(data) > 0 {
			s.Errorf("set credentials_json or credentials_file, not both")
		}
		var err error
		if data, err = os.ReadFile(f); err != nil {
			s.Errorf("credentials_file: %v", err)
			return nil, nil
		}
	}
	base := builtin.HTTPClient(s)
	if base == nil {
		base = notijet.DefaultHTTPClient()
	}
	// Token refreshes outlive the build context, so they get their own.
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, base)
	if len(data) == 0 {
		if subject != "" {
			s.Errorf("user needs a service account key (credentials_json or credentials_file)")
			return nil, nil
		}
		creds, err := google.FindDefaultCredentials(ctx, scope)
		if err != nil {
			return nil, notijet.Invalid("config", "provider "+s.Name()+": no credentials: "+err.Error())
		}
		return authorized(tokenCtx, base, creds.TokenSource), nil
	}
	jwt, err := google.JWTConfigFromJSON(data, scope)
	if err != nil {
		s.Errorf("service account key: %v", err)
		return nil, nil
	}
	jwt.Subject = subject
	return authorized(tokenCtx, base, jwt.TokenSource(tokenCtx)), nil
}

func authorized(ctx context.Context, base *http.Client, ts oauth2.TokenSource) *http.Client {
	hc := oauth2.NewClient(ctx, oauth2.ReuseTokenSource(nil, ts))
	hc.Timeout = base.Timeout
	return hc
}
