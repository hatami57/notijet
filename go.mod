module github.com/hatami57/notijet

go 1.26.2

// notijet builds against a local ../microjet checkout via these replace
// directives so it tracks in-progress framework changes. To release notijet for
// external `go get` against the published microjet modules, drop these replaces.
replace (
	github.com/hatami57/microjet/aws => ../microjet/aws
	github.com/hatami57/microjet/core => ../microjet/core
	github.com/hatami57/microjet/gormx => ../microjet/gormx
	github.com/hatami57/microjet/gormx/sqlite => ../microjet/gormx/sqlite
	github.com/hatami57/microjet/host => ../microjet/host
)

require (
	github.com/aws/aws-sdk-go-v2 v1.47.3
	github.com/aws/aws-sdk-go-v2/service/sns v1.47.5
	github.com/aws/smithy-go v1.28.5
	github.com/hatami57/microjet/core v0.41.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.6 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.6 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
