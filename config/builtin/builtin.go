// Package builtin registers every provider that ships with notijet in a
// config.Registry, so they can be configured by type name:
//
//	b := &config.Builder{Registry: builtin.Registry()}
//	built, err := b.Build(ctx, specs)
//
// It imports all provider packages (and the AWS SDK and OAuth2 for the
// providers that need them); applications that configure only a few
// providers can register those factories themselves instead.
//
// Settings per type (* marks required; every HTTP provider also takes
// timeout, a Go duration bounding each request):
//
//	kavenegar    api_key*, sender, base_url
//	smsir        api_key*, sender, base_url
//	melipayamak  username*, password*, sender, base_url
//	ghasedak     api_key*, sender, base_url
//	ippanel      api_key*, sender, base_url
//	twilio       account_sid*, auth_token* (or api_key_sid* + api_key_secret*), sender,
//	             default_country, status_callback, base_url
//	vonage       api_key*, api_secret*, sender, default_country, callback, base_url
//	plivo        auth_id*, auth_token*, sender, default_country, callback, base_url
//	infobip      base_url*, api_key*, sender, default_country, notify_url
//	sns          region, access_key_id, secret_access_key, endpoint, sender, sms_type,
//	             default_country (credentials default to the AWS chain)
//	smtp         preset (gmail, outlook, office365, yahoo, zoho, icloud, yandex) or host*,
//	             port, security (starttls, tls, none), username, password, from,
//	             local_name, message_id_domain
//	sendgrid     api_key*, from, region (us, eu), base_url
//	mailgun      domain*, api_key*, from, region (us, eu), base_url
//	postmark     server_token*, from, message_stream, base_url
//	resend       api_key*, from, base_url
//	brevo        api_key*, from, base_url
//	ses          region, access_key_id, secret_access_key, endpoint, from, configuration_set
//	gmail        credentials_file or credentials_json (service account), user, from, base_url
//	             (without credentials, Application Default Credentials)
//	telegram     bot_token*, chat_id, silent, disable_link_preview, base_url
//	bale         bot_token*, chat_id, base_url
//	slack        webhook_url*
//	discord      webhook_url*, username, avatar_url, mentions
//	fcm          project_id*, credentials_file or credentials_json, base_url
//	webhook      url*, secret, channel, header.<Name> (one per header)
//	logger       channel, level (debug, info, warn, error)
//	memory       channel
package builtin

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/chat/bale"
	"github.com/hatami57/notijet/chat/discord"
	"github.com/hatami57/notijet/chat/slack"
	"github.com/hatami57/notijet/chat/telegram"
	"github.com/hatami57/notijet/config"
	"github.com/hatami57/notijet/email/brevo"
	"github.com/hatami57/notijet/email/gmail"
	"github.com/hatami57/notijet/email/mailgun"
	"github.com/hatami57/notijet/email/postmark"
	"github.com/hatami57/notijet/email/resend"
	"github.com/hatami57/notijet/email/sendgrid"
	"github.com/hatami57/notijet/email/ses"
	"github.com/hatami57/notijet/email/smtp"
	"github.com/hatami57/notijet/logger"
	"github.com/hatami57/notijet/memory"
	"github.com/hatami57/notijet/push/fcm"
	"github.com/hatami57/notijet/sms/ghasedak"
	"github.com/hatami57/notijet/sms/infobip"
	"github.com/hatami57/notijet/sms/ippanel"
	"github.com/hatami57/notijet/sms/kavenegar"
	"github.com/hatami57/notijet/sms/melipayamak"
	"github.com/hatami57/notijet/sms/plivo"
	"github.com/hatami57/notijet/sms/smsir"
	"github.com/hatami57/notijet/sms/sns"
	"github.com/hatami57/notijet/sms/twilio"
	"github.com/hatami57/notijet/sms/vonage"
	"github.com/hatami57/notijet/webhook"
)

// OAuth2 scopes the Google providers request.
const (
	GmailScope = "https://www.googleapis.com/auth/gmail.send"
	FCMScope   = "https://www.googleapis.com/auth/firebase.messaging"
)

// Registry returns a registry with every built-in provider type.
func Registry() *config.Registry {
	r := config.NewRegistry()
	for typ, f := range factories {
		r.Register(typ, f)
	}
	return r
}

var factories = map[string]config.Factory{
	"kavenegar": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []kavenegar.Option{kavenegar.WithHTTPClient(client(s)), kavenegar.WithSender(s.String("sender"))}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, kavenegar.WithBaseURL(u))
		}
		return kavenegar.New(s.Required("api_key"), opts...), nil
	},
	"smsir": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []smsir.Option{smsir.WithHTTPClient(client(s)), smsir.WithSender(s.String("sender"))}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, smsir.WithBaseURL(u))
		}
		return smsir.New(s.Required("api_key"), opts...), nil
	},
	"melipayamak": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []melipayamak.Option{melipayamak.WithHTTPClient(client(s)), melipayamak.WithSender(s.String("sender"))}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, melipayamak.WithBaseURL(u))
		}
		return melipayamak.New(s.Required("username"), s.Required("password"), opts...), nil
	},
	"ghasedak": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []ghasedak.Option{ghasedak.WithHTTPClient(client(s)), ghasedak.WithSender(s.String("sender"))}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, ghasedak.WithBaseURL(u))
		}
		return ghasedak.New(s.Required("api_key"), opts...), nil
	},
	"ippanel": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []ippanel.Option{ippanel.WithHTTPClient(client(s)), ippanel.WithSender(s.String("sender"))}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, ippanel.WithBaseURL(u))
		}
		return ippanel.New(s.Required("api_key"), opts...), nil
	},
	"twilio": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []twilio.Option{
			twilio.WithHTTPClient(client(s)),
			twilio.WithSender(s.String("sender")),
			twilio.WithDefaultCountry(s.String("default_country")),
			twilio.WithStatusCallback(s.String("status_callback")),
		}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, twilio.WithBaseURL(u))
		}
		sid := s.Required("account_sid")
		token, keySID, keySecret := s.String("auth_token"), s.String("api_key_sid"), s.String("api_key_secret")
		switch {
		case keySID != "" || keySecret != "":
			if keySID == "" || keySecret == "" {
				s.Errorf("api_key_sid and api_key_secret go together")
			}
			opts = append(opts, twilio.WithAPIKey(keySID, keySecret))
		case token == "":
			s.Errorf("auth_token (or api_key_sid and api_key_secret) is required")
		}
		return twilio.New(sid, token, opts...), nil
	},
	"vonage": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []vonage.Option{
			vonage.WithHTTPClient(client(s)),
			vonage.WithSender(s.String("sender")),
			vonage.WithDefaultCountry(s.String("default_country")),
			vonage.WithCallback(s.String("callback")),
		}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, vonage.WithBaseURL(u))
		}
		return vonage.New(s.Required("api_key"), s.Required("api_secret"), opts...), nil
	},
	"plivo": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []plivo.Option{
			plivo.WithHTTPClient(client(s)),
			plivo.WithSender(s.String("sender")),
			plivo.WithDefaultCountry(s.String("default_country")),
			plivo.WithCallback(s.String("callback")),
		}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, plivo.WithBaseURL(u))
		}
		return plivo.New(s.Required("auth_id"), s.Required("auth_token"), opts...), nil
	},
	"infobip": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		return infobip.New(s.Required("base_url"), s.Required("api_key"),
			infobip.WithHTTPClient(client(s)),
			infobip.WithSender(s.String("sender")),
			infobip.WithDefaultCountry(s.String("default_country")),
			infobip.WithNotifyURL(s.String("notify_url")),
		), nil
	},
	"sns": func(ctx context.Context, s *config.Settings) (notijet.Provider, error) {
		cfg, endpoint, err := awsConfig(ctx, s)
		smsType := s.OneOf("sms_type", "transactional", "promotional")
		opts := []sns.Option{sns.WithSender(s.String("sender")), sns.WithDefaultCountry(s.String("default_country"))}
		switch smsType {
		case "transactional":
			opts = append(opts, sns.WithSMSType(sns.Transactional))
		case "promotional":
			opts = append(opts, sns.WithSMSType(sns.Promotional))
		}
		if err != nil {
			return nil, err
		}
		api := awssns.NewFromConfig(cfg, func(o *awssns.Options) {
			if endpoint != "" {
				o.BaseEndpoint = aws.String(endpoint)
			}
		})
		return sns.New(api, opts...), nil
	},
	"smtp": smtpFactory,
	"sendgrid": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []sendgrid.Option{sendgrid.WithHTTPClient(client(s)), sendgrid.WithFrom(s.String("from"))}
		if s.OneOf("region", "us", "eu") == "eu" {
			opts = append(opts, sendgrid.WithBaseURL(sendgrid.EUBaseURL))
		}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, sendgrid.WithBaseURL(u))
		}
		return sendgrid.New(s.Required("api_key"), opts...), nil
	},
	"mailgun": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []mailgun.Option{mailgun.WithHTTPClient(client(s)), mailgun.WithFrom(s.String("from"))}
		if s.OneOf("region", "us", "eu") == "eu" {
			opts = append(opts, mailgun.WithBaseURL(mailgun.EUBaseURL))
		}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, mailgun.WithBaseURL(u))
		}
		return mailgun.New(s.Required("domain"), s.Required("api_key"), opts...), nil
	},
	"postmark": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []postmark.Option{
			postmark.WithHTTPClient(client(s)),
			postmark.WithFrom(s.String("from")),
			postmark.WithMessageStream(s.String("message_stream")),
		}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, postmark.WithBaseURL(u))
		}
		return postmark.New(s.Required("server_token"), opts...), nil
	},
	"resend": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []resend.Option{resend.WithHTTPClient(client(s)), resend.WithFrom(s.String("from"))}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, resend.WithBaseURL(u))
		}
		return resend.New(s.Required("api_key"), opts...), nil
	},
	"brevo": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []brevo.Option{brevo.WithHTTPClient(client(s)), brevo.WithFrom(s.String("from"))}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, brevo.WithBaseURL(u))
		}
		return brevo.New(s.Required("api_key"), opts...), nil
	},
	"ses": func(ctx context.Context, s *config.Settings) (notijet.Provider, error) {
		cfg, endpoint, err := awsConfig(ctx, s)
		opts := []ses.Option{ses.WithFrom(s.String("from")), ses.WithConfigurationSet(s.String("configuration_set"))}
		if err != nil {
			return nil, err
		}
		api := sesv2.NewFromConfig(cfg, func(o *sesv2.Options) {
			if endpoint != "" {
				o.BaseEndpoint = aws.String(endpoint)
			}
		})
		return ses.New(api, opts...), nil
	},
	"gmail": func(ctx context.Context, s *config.Settings) (notijet.Provider, error) {
		user := s.String("user")
		opts := []gmail.Option{gmail.WithFrom(s.String("from"))}
		if user != "" {
			opts = append(opts, gmail.WithUser(user))
		}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, gmail.WithBaseURL(u))
		}
		hc, err := googleClient(ctx, s, GmailScope, user)
		if err != nil {
			return nil, err
		}
		return gmail.New(append(opts, gmail.WithHTTPClient(hc))...), nil
	},
	"telegram": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []telegram.Option{telegram.WithHTTPClient(client(s)), telegram.WithChatID(s.String("chat_id"))}
		if s.Bool("silent") {
			opts = append(opts, telegram.WithSilent())
		}
		if s.Bool("disable_link_preview") {
			opts = append(opts, telegram.WithoutLinkPreview())
		}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, telegram.WithBaseURL(u))
		}
		return telegram.New(s.Required("bot_token"), opts...), nil
	},
	"bale": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []bale.Option{bale.WithHTTPClient(client(s)), bale.WithChatID(s.String("chat_id"))}
		if u := s.String("base_url"); u != "" {
			opts = append(opts, bale.WithBaseURL(u))
		}
		return bale.New(s.Required("bot_token"), opts...), nil
	},
	"slack": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		return slack.New(s.Required("webhook_url"), slack.WithHTTPClient(client(s))), nil
	},
	"discord": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []discord.Option{
			discord.WithHTTPClient(client(s)),
			discord.WithUsername(s.String("username")),
			discord.WithAvatarURL(s.String("avatar_url")),
		}
		if s.Bool("mentions") {
			opts = append(opts, discord.WithMentions())
		}
		return discord.New(s.Required("webhook_url"), opts...), nil
	},
	"fcm": func(ctx context.Context, s *config.Settings) (notijet.Provider, error) {
		project := s.Required("project_id")
		var opts []fcm.Option
		if u := s.String("base_url"); u != "" {
			opts = append(opts, fcm.WithBaseURL(u))
		}
		hc, err := googleClient(ctx, s, FCMScope, "")
		if err != nil {
			return nil, err
		}
		return fcm.New(project, append(opts, fcm.WithHTTPClient(hc))...), nil
	},
	"webhook": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []webhook.Option{webhook.WithHTTPClient(client(s)), webhook.WithSecret(s.String("secret"))}
		if ch := s.String("channel"); ch != "" {
			opts = append(opts, webhook.WithChannel(notijet.Channel(strings.ToLower(ch))))
		}
		for k, v := range s.Prefixed("header.") {
			opts = append(opts, webhook.WithHeader(k, v))
		}
		return webhook.New(s.Required("url"), opts...), nil
	},
	"logger": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []logger.Option{logger.WithName(s.Name())}
		if ch := s.String("channel"); ch != "" {
			opts = append(opts, logger.WithChannel(notijet.Channel(strings.ToLower(ch))))
		}
		switch s.OneOf("level", "debug", "info", "warn", "error") {
		case "debug":
			opts = append(opts, logger.WithLevel(slog.LevelDebug))
		case "warn":
			opts = append(opts, logger.WithLevel(slog.LevelWarn))
		case "error":
			opts = append(opts, logger.WithLevel(slog.LevelError))
		}
		return logger.New(nil, opts...), nil
	},
	"memory": func(_ context.Context, s *config.Settings) (notijet.Provider, error) {
		opts := []memory.Option{memory.WithName(s.Name())}
		if ch := s.String("channel"); ch != "" {
			opts = append(opts, memory.WithChannel(notijet.Channel(strings.ToLower(ch))))
		}
		return memory.New(opts...), nil
	},
}

func smtpFactory(_ context.Context, s *config.Settings) (notijet.Provider, error) {
	var opts []smtp.Option
	switch s.OneOf("security", "starttls", "tls", "none") {
	case "starttls":
		opts = append(opts, smtp.WithSecurity(smtp.StartTLS))
	case "tls":
		opts = append(opts, smtp.WithSecurity(smtp.ImplicitTLS))
	case "none":
		opts = append(opts, smtp.WithSecurity(smtp.NoTLS))
	}
	if user, pass := s.String("username"), s.String("password"); user != "" || pass != "" {
		if user == "" {
			s.Errorf("password needs a username")
		}
		opts = append(opts, smtp.WithAuth(user, pass))
	}
	opts = append(opts, smtp.WithFrom(s.String("from")), smtp.WithMessageIDDomain(s.String("message_id_domain")))
	if n := s.String("local_name"); n != "" {
		opts = append(opts, smtp.WithLocalName(n))
	}
	if d := s.Duration("timeout"); d > 0 {
		opts = append(opts, smtp.WithTimeout(d))
	}
	port := s.Int("port")
	if preset := s.OneOf("preset", "gmail", "outlook", "office365", "yahoo", "zoho", "icloud", "yandex"); preset != "" {
		ps := map[string]smtp.Preset{
			"gmail": smtp.Gmail, "outlook": smtp.Outlook, "office365": smtp.Office365,
			"yahoo": smtp.Yahoo, "zoho": smtp.Zoho, "icloud": smtp.ICloud, "yandex": smtp.Yandex,
		}[preset]
		if host := s.String("host"); host != "" {
			s.Errorf("set preset or host, not both")
		}
		if port != 0 {
			ps.Port = port
		}
		// The preset's security comes first so an explicit one overrides it.
		return ps.New(opts...), nil
	}
	return smtp.New(s.Required("host"), port, opts...), nil
}

// client returns an HTTP client bounded by the timeout setting, or nil for
// the provider's default.
func client(s *config.Settings) *http.Client {
	if d := s.Duration("timeout"); d > 0 {
		return &http.Client{Timeout: d}
	}
	return nil
}

// awsConfig loads the AWS configuration: the default chain (environment,
// shared files, instance roles), with region and static keys when set.
func awsConfig(ctx context.Context, s *config.Settings) (aws.Config, string, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if r := s.String("region"); r != "" {
		opts = append(opts, awsconfig.WithRegion(r))
	}
	if id, secret := s.String("access_key_id"), s.String("secret_access_key"); id != "" || secret != "" {
		if id == "" || secret == "" {
			s.Errorf("access_key_id and secret_access_key go together")
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(id, secret, "")))
	}
	if hc := client(s); hc != nil {
		opts = append(opts, awsconfig.WithHTTPClient(hc))
	}
	endpoint := s.String("endpoint")
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, "", notijet.Invalid("config", "provider "+s.Name()+": loading AWS configuration: "+err.Error())
	}
	return cfg, endpoint, nil
}

// googleClient returns an HTTP client authorized for scope: from a service
// account key (credentials_json or credentials_file), impersonating subject
// when set, or from Application Default Credentials.
func googleClient(ctx context.Context, s *config.Settings, scope, subject string) (*http.Client, error) {
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
	base := client(s)
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
