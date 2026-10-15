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

// Classify maps the errors every AWS-backed provider treats alike: a non-API
// error (transport failure, cancelled context) is a fault, and credential,
// throttling and server-side errors get their category. For those it returns
// the notijet error as mapped. For any other API error mapped is nil and
// apiErr is the error for the provider to map from its code.
func Classify(provider string, err error) (apiErr smithy.APIError, mapped error) {
	if !errors.As(err, &apiErr) {
		return nil, notijet.Fault(provider, "AWS call failed", err)
	}
	code, message := apiErr.ErrorCode(), apiErr.ErrorMessage()
	switch {
	case authCodes[code]:
		return apiErr, notijet.Rejected(provider, code, message, notijet.ErrUnauthorized)
	case throttleCodes[code]:
		return apiErr, notijet.Throttled(provider, code, message)
	case apiErr.ErrorFault() == smithy.FaultServer:
		return apiErr, notijet.FaultCode(provider, code, message, err)
	}
	return apiErr, nil
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
