package notijet

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// Failover returns a provider that tries each of providers in order and returns
// the first success. Result.Provider names the provider that delivered.
//
// It moves on after any error — a refusal (no credit, bad credentials), a
// fault, or a feature the provider lacks (a Template on a text-only provider is
// fine when the message also carries Text; see TextOrTemplate). It stops when
// ctx is done, and on a *PartialError, since the next provider would resend to
// the recipients already reached. Wrap each member in Retry to retry a provider
// before moving on, or wrap the Failover in Retry to retry the whole chain.
//
// The Failover is named "failover" and reports the first provider's Channel.
// It panics when providers is empty or holds a nil provider.
func Failover(providers ...Provider) Provider {
	if len(providers) == 0 {
		panic("notijet: Failover needs at least one provider")
	}
	for _, p := range providers {
		if p == nil {
			panic("notijet: Failover given a nil provider")
		}
	}
	return &failover{providers: append([]Provider(nil), providers...)}
}

type failover struct {
	providers []Provider
}

func (f *failover) Name() string     { return "failover" }
func (f *failover) Channel() Channel { return f.providers[0].Channel() }

func (f *failover) Send(ctx context.Context, msg *Message) (*Result, error) {
	var errs []error
	for _, p := range f.providers {
		if len(errs) > 0 && ctx.Err() != nil {
			break
		}
		res, err := p.Send(ctx, msg)
		if err == nil {
			if res == nil {
				res = &Result{}
			}
			if res.Provider == "" {
				res.Provider = p.Name()
			}
			return res, nil
		}
		errs = append(errs, err)
		if _, partial := errors.AsType[*PartialError](err); partial {
			break
		}
	}
	if len(errs) == 1 {
		return nil, errs[0]
	}
	return nil, &FailoverError{Errors: errs}
}

// FailoverError is returned when more than one provider of a Failover failed.
// Errors holds each provider's error in the order they were tried.
//
// It unwraps to a single representative error, so errorx categories,
// errors.Is and IsRetryable see that one: a *PartialError when the chain
// stopped on one (the message is partly delivered, so it must not be retried
// whole), else the first retryable error — so a queue retries a chain that
// failed partly for transient reasons — and otherwise the last error. Inspect Errors to examine every failure.
type FailoverError struct {
	Errors []error
}

func (e *FailoverError) Error() string {
	if e == nil {
		return "<nil failover error>"
	}
	var b strings.Builder
	b.WriteString("all ")
	b.WriteString(strconv.Itoa(len(e.Errors)))
	b.WriteString(" providers failed: ")
	for i, err := range e.Errors {
		if i > 0 {
			b.WriteString("; ")
		}
		if err == nil {
			b.WriteString("<nil error>")
		} else {
			b.WriteString(err.Error())
		}
	}
	return b.String()
}

// Unwrap returns the representative error described on FailoverError.
func (e *FailoverError) Unwrap() error {
	if e == nil || len(e.Errors) == 0 {
		return nil
	}
	last := e.Errors[len(e.Errors)-1]
	if _, partial := errors.AsType[*PartialError](last); partial {
		return last
	}
	for _, err := range e.Errors {
		if IsRetryable(err) {
			return err
		}
	}
	return last
}
