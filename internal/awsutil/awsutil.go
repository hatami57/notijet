// Package awsutil classifies errors from the AWS SDK for the providers built
// on it (sms/sns, email/ses).
package awsutil

import (
	"errors"

	"github.com/aws/smithy-go"

	"github.com/hatami57/notijet"
)

// authCodes are the error codes AWS services use for bad, expired or
// insufficient credentials.
var authCodes = map[string]bool{
	"AuthorizationError": true, "AccessDenied": true, "AccessDeniedException": true,
	"InvalidClientTokenId": true, "SignatureDoesNotMatch": true, "UnrecognizedClientException": true,
	"ExpiredToken": true, "ExpiredTokenException": true, "MissingAuthenticationToken": true,
}

// throttleCodes are the error codes AWS services use for request throttling.
var throttleCodes = map[string]bool{
	"Throttled": true, "Throttling": true, "ThrottlingException": true, "ThrottledException": true,
	"TooManyRequestsException": true, "RequestLimitExceeded": true,
}

// Classify handles the errors every AWS-backed provider treats alike. It
// returns the notijet error and true for a non-API error (transport failure,
// cancelled context: a fault), a credentials error, a throttling error or a
// server-side fault. For any other API error it returns the error code and
// message and false, for the provider to map.
func Classify(provider string, err error) (mapped error, code, message string, done bool) {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return notijet.Fault(provider, "AWS call failed", err), "", "", true
	}
	code, message = apiErr.ErrorCode(), apiErr.ErrorMessage()
	switch {
	case authCodes[code]:
		return notijet.Rejected(provider, code, message, notijet.ErrUnauthorized), code, message, true
	case throttleCodes[code]:
		return notijet.Throttled(provider, code, message), code, message, true
	case apiErr.ErrorFault() == smithy.FaultServer:
		return notijet.FaultCode(provider, code, message, err), code, message, true
	}
	return nil, code, message, false
}

// ValidTag reports whether s may be an SNS/SES/Resend-style tag name or
// value: 1–256 ASCII letters, digits, '_' or '-'.
func ValidTag(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
