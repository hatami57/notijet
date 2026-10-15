// Package phone normalizes phone numbers for SMS providers. Users type numbers
// in many shapes — Persian or Arabic digits, spaces and dashes, +98, 0098, 98
// or a leading 0 — and each provider wants one of them. The helpers here turn
// any of those into the shape a provider needs.
package phone

import (
	"errors"
	"strings"
)

// ErrInvalid is returned for a value that is not a phone number in the
// requested shape.
var ErrInvalid = errors.New("invalid phone number")

// IranCountryCode is Iran's calling code.
const IranCountryCode = "98"

// EnglishDigits replaces Persian (۰-۹) and Arabic-Indic (٠-٩) digits with ASCII
// digits, leaving every other character as it is.
func EnglishDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s)
}

// Clean converts digits to ASCII and drops the separators people type into
// phone numbers (spaces, dashes, dots, parentheses, slashes). A leading + is
// kept. Any other character is kept too, so a value that is not a number stays
// recognisably invalid.
func Clean(s string) string {
	s = EnglishDigits(strings.TrimSpace(s))
	var b strings.Builder
	for i, r := range s {
		switch r {
		case ' ', '-', '.', '(', ')', '/', ' ', '‌', '‎', '‏':
			continue
		case '+':
			if i != 0 {
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// IranMobile returns s as an Iranian mobile number in the local 11-digit form
// (09121234567). It accepts +989121234567, 00989121234567, 989121234567,
// 09121234567 and 9121234567, with any digits and separators Clean handles.
func IranMobile(s string) (string, error) {
	n := Clean(s)
	switch {
	case strings.HasPrefix(n, "+98"):
		n = n[3:]
	case strings.HasPrefix(n, "0098"):
		n = n[4:]
	case strings.HasPrefix(n, "98") && len(n) == 12:
		n = n[2:]
	case strings.HasPrefix(n, "0"):
		n = n[1:]
	}
	if len(n) != 10 || n[0] != '9' || !allDigits(n) {
		return "", ErrInvalid
	}
	return "0" + n, nil
}

// IsIranMobile reports whether s is an Iranian mobile number in any shape
// IranMobile accepts.
func IsIranMobile(s string) bool {
	_, err := IranMobile(s)
	return err == nil
}

// E164 returns s in E.164 form (+14155550100). A number written with + or 00
// keeps its country code; a number with a single leading 0 (a national trunk
// prefix) gets defaultCountryCode in its place; any other number is taken to
// already start with its country code. defaultCountryCode is digits without a
// plus ("98", "1"); empty means numbers without a country code are rejected.
func E164(s, defaultCountryCode string) (string, error) {
	n := Clean(s)
	switch {
	case strings.HasPrefix(n, "+"):
		n = n[1:]
	case strings.HasPrefix(n, "00"):
		n = n[2:]
	case strings.HasPrefix(n, "0"):
		if defaultCountryCode == "" {
			return "", ErrInvalid
		}
		n = strings.TrimPrefix(defaultCountryCode, "+") + n[1:]
	}
	// E.164 numbers are at most 15 digits; 8 is a practical lower bound that
	// still admits the shortest national numbers with their country code.
	if len(n) < 8 || len(n) > 15 || n[0] == '0' || !allDigits(n) {
		return "", ErrInvalid
	}
	return "+" + n, nil
}

// International returns s in E.164 form without the leading plus
// (14155550100), the shape several SMS APIs want.
func International(s, defaultCountryCode string) (string, error) {
	n, err := E164(s, defaultCountryCode)
	if err != nil {
		return "", err
	}
	return n[1:], nil
}

// IranLocalOrInternational returns an Iranian mobile in local form
// (09121234567) and any other number with a 00 international prefix
// (00447700900123): the shapes Iranian SMS gateways accept.
func IranLocalOrInternational(s string) (string, error) {
	if n, err := IranMobile(s); err == nil {
		return n, nil
	}
	n, err := International(s, IranCountryCode)
	if err != nil {
		return "", err
	}
	return "00" + n, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
