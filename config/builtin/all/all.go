// Package all registers every provider type that ships with notijet in one
// config.Registry: config/builtin's types plus those of config/builtin/aws
// and config/builtin/google. It links the AWS SDK and OAuth2; applications
// that need neither should use builtin.Registry() instead.
//
//	module.New(module.WithConfig(all.Registry()))
package all

import (
	"github.com/hatami57/notijet/config"
	"github.com/hatami57/notijet/config/builtin"
	"github.com/hatami57/notijet/config/builtin/aws"
	"github.com/hatami57/notijet/config/builtin/google"
)

// Registry returns a registry with every built-in provider type.
func Registry() *config.Registry {
	return google.Register(aws.Register(builtin.Registry()))
}
