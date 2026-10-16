// Package aws registers the notijet providers built on the AWS SDK — sns
// (SMS) and ses (email) — in a config.Registry. They live apart from
// config/builtin so applications that do not use them do not link the SDK:
//
//	r := aws.Register(builtin.Registry())
//
// Settings per type (credentials default to the AWS chain: environment,
// shared files, instance roles; timeout bounds each request):
//
//	sns  region, access_key_id, secret_access_key, endpoint, sender, sms_type
//	     (transactional, promotional), default_country, timeout
//	ses  region, access_key_id, secret_access_key, endpoint, from,
//	     configuration_set, timeout
package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	awssns "github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/hatami57/notijet"
	"github.com/hatami57/notijet/config"
	"github.com/hatami57/notijet/config/builtin"
	"github.com/hatami57/notijet/email/ses"
	"github.com/hatami57/notijet/sms/sns"
)

// Register adds the sns and ses types to r and returns it.
func Register(r *config.Registry) *config.Registry {
	return r.Register("sns", SNS).Register("ses", SES)
}

// SNS builds an Amazon SNS SMS provider.
func SNS(ctx context.Context, s *config.Settings) (notijet.Provider, error) {
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
}

// SES builds an Amazon SES email provider.
func SES(ctx context.Context, s *config.Settings) (notijet.Provider, error) {
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
	if hc := builtin.HTTPClient(s); hc != nil {
		opts = append(opts, awsconfig.WithHTTPClient(hc))
	}
	endpoint := s.String("endpoint")
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, "", notijet.Invalid("config", "provider "+s.Name()+": loading AWS configuration: "+err.Error())
	}
	return cfg, endpoint, nil
}
