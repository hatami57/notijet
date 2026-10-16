package notijet

import (
	"context"
	"errors"

	"github.com/hatami57/microjet/core/errorx"
)

// Sentinel errors for outcomes a caller branches on. They are carried as the
// Inner of the *errorx.Error the helpers below return, so errors.Is keeps
// working while the error also has a category that microjet's HTTP error
// middleware maps to a status code.
var (
	// ErrUnauthorized means the provider rejected the credentials (API key,
	// token, username/password), or the account is disabled.
	ErrUnauthorized = errors.New("provider rejected the credentials")
	// ErrInsufficientCredit means the account has no credit left to send.
	ErrInsufficientCredit = errors.New("insufficient provider credit")
	// ErrInvalidRecipient means a recipient (phone number, address, chat ID,
	// device token) is malformed, unknown or blocked.
	ErrInvalidRecipient = errors.New("invalid recipient")
	// ErrInvalidSender means the sender (line number, From address, sender ID)
	// is not one the account may send from.
	ErrInvalidSender = errors.New("invalid sender")
	// ErrTemplate means the provider template is unknown, unapproved, or does
	// not match the parameters given.
	ErrTemplate = errors.New("invalid provider template")
	// ErrRateLimited means the provider throttled the request. It is retryable:
	// send again after a pause.
	ErrRateLimited = errors.New("provider rate limit exceeded")
	// ErrUnsupported means the message uses a feature the provider cannot
	// honour, such as an attachment on SMS or a template on a provider without
	// server-side templates.
	ErrUnsupported = errors.New("feature not supported by provider")
	// ErrNoProvider means a Notifier has no provider under the requested name or
	// for the requested channel.
	ErrNoProvider = errors.New("no such provider")
)

// providerError builds the structured *errorx.Error shared by the helpers below.
// The provider name becomes the error Subject, and code — the provider's own
// status or error code — is attached as the providerCode param. microjet's HTTP
// error middleware does not log Params, so detail that must reach the logs
// belongs in inner.
func providerError(typ errorx.ErrorType, provider, code, message string, inner error) *errorx.Error {
	e := errorx.NewError(typ, provider, message)
	if code != "" {
		e = e.WithParams("providerCode", code)
	}
	if inner != nil {
		e = e.WithInner(inner)
	}
	return e
}

// Invalid reports a bad caller-supplied message detected before any network
// call (no recipient, empty body, malformed phone number). BadRequest → 400.
func Invalid(provider, message string) *errorx.Error {
	return providerError(errorx.BadRequestErrorType, provider, "", message, nil)
}

// InvalidRecipient reports a malformed recipient detected before any network
// call. BadRequest → 400, and matches errors.Is(err, ErrInvalidRecipient).
func InvalidRecipient(provider, message string) *errorx.Error {
	return providerError(errorx.BadRequestErrorType, provider, "", message, ErrInvalidRecipient)
}

// InvalidSender reports a malformed sender detected before any network call.
// BadRequest → 400, and matches errors.Is(err, ErrInvalidSender).
func InvalidSender(provider, message string) *errorx.Error {
	return providerError(errorx.BadRequestErrorType, provider, "", message, ErrInvalidSender)
}

// InvalidTemplate reports a malformed provider template detected before any
// network call (a non-numeric ID where the provider wants a number, a missing
// parameter). BadRequest → 400, and matches errors.Is(err, ErrTemplate).
func InvalidTemplate(provider, message string) *errorx.Error {
	return providerError(errorx.BadRequestErrorType, provider, "", message, ErrTemplate)
}

// Unsupported reports a message feature the provider cannot honour.
// BadRequest → 400, and matches errors.Is(err, ErrUnsupported).
func Unsupported(provider, feature string) *errorx.Error {
	return providerError(errorx.BadRequestErrorType, provider, "", feature+" is not supported", ErrUnsupported)
}

// Rejected reports that the provider refused a well-formed request with an
// error code (bad credentials, no credit, invalid sender ...). It is a Business
// error (409) and permanent: sending the same message again will not help.
// sentinel, when not nil, classifies the refusal (ErrUnauthorized,
// ErrInsufficientCredit ...) for errors.Is.
func Rejected(provider, code, message string, sentinel error) *errorx.Error {
	if message == "" {
		message = "provider rejected the message"
	}
	return providerError(errorx.BusinessErrorType, provider, code, message, sentinel)
}

// Throttled reports that the provider rate-limited the request. It is an
// Internal error (retryable) matching errors.Is(err, ErrRateLimited).
func Throttled(provider, code, message string) *errorx.Error {
	if message == "" {
		message = ErrRateLimited.Error()
	}
	return providerError(errorx.InternalErrorType, provider, code, message, ErrRateLimited)
}

// Fault reports an unexpected failure talking to the provider — a transport
// error, a 5xx reply, an unreadable body. Internal → 500, and retryable: it
// says nothing about the message itself. inner may be nil.
func Fault(provider, message string, inner error) *errorx.Error {
	if message == "" {
		message = "provider call failed"
	}
	return providerError(errorx.InternalErrorType, provider, "", message, inner)
}

// FaultCode is Fault with the provider's own status code attached.
func FaultCode(provider, code, message string, inner error) *errorx.Error {
	if message == "" {
		message = "provider call failed"
	}
	return providerError(errorx.InternalErrorType, provider, code, message, inner)
}

// IsRetryable reports whether sending the same message again may succeed: a
// fault (transport failure, 5xx, unreadable reply) or a rate limit. Bad input,
// unsupported features and provider refusals are permanent, and so is a
// cancelled or deadline-exceeded context — the caller gave up — and a *PartialError, since sending
// again would duplicate what was delivered. A plain error that is not an
// errorx.Error (from a custom provider) is treated as retryable.
func IsRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, partial := errors.AsType[*PartialError](err); partial {
		return false
	}
	if errors.Is(err, ErrRateLimited) {
		return true
	}
	typ, ok := errorx.GetErrorType(err)
	if !ok {
		return true
	}
	return typ == errorx.InternalErrorType
}

// ProviderCode returns the provider's own error code attached to err (the
// providerCode param), or "" when there is none.
func ProviderCode(err error) string {
	e := errorx.GetError(err)
	if e == nil {
		return ""
	}
	if code, ok := e.Params["providerCode"].(string); ok {
		return code
	}
	return ""
}
