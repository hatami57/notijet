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
