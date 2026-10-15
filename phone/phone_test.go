package phone

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnglishDigits(t *testing.T) {
	assert.Equal(t, "09121234567", EnglishDigits("۰۹۱۲۱۲۳۴۵۶۷"))
	assert.Equal(t, "0912", EnglishDigits("٠٩١٢"))
	assert.Equal(t, "abc 12", EnglishDigits("abc ۱2"))
}

func TestClean(t *testing.T) {
	assert.Equal(t, "+989121234567", Clean(" +98 (912) 123-45.67 "))
	assert.Equal(t, "09121234567", Clean("۰۹۱۲ ۱۲۳ ۴۵۶۷"))
	assert.Equal(t, "+12", Clean("+1+2"), "only a leading plus survives")
}

func TestIranMobile(t *testing.T) {
	for _, in := range []string{
		"09121234567", "9121234567", "+989121234567", "00989121234567",
		"989121234567", "۰۹۱۲۱۲۳۴۵۶۷", "+98 912 123 4567",
	} {
		got, err := IranMobile(in)
		require.NoError(t, err, in)
		assert.Equal(t, "09121234567", got, in)
	}
	for _, in := range []string{"", "0912123456", "091212345678", "02112345678", "+14155550100", "0912abc4567"} {
		_, err := IranMobile(in)
		assert.ErrorIs(t, err, ErrInvalid, in)
	}
	assert.True(t, IsIranMobile("+989121234567"))
	assert.False(t, IsIranMobile("+14155550100"))
}

func TestE164(t *testing.T) {
	cases := []struct{ in, cc, want string }{
		{"09121234567", "98", "+989121234567"},
		{"+98 912 123 4567", "", "+989121234567"},
		{"00989121234567", "", "+989121234567"},
		{"14155550100", "", "+14155550100"},
		{"(415) 555-0100", "1", "+4155550100"}, // no trunk 0: taken as already international
		{"0415 555 0100", "+1", "+14155550100"},
	}
	for _, c := range cases {
		got, err := E164(c.in, c.cc)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.want, got, c.in)
	}
	for _, in := range []string{"", "0912", "09121234567", "+0123456789", "+1234567890123456", "+98912abc"} {
		_, err := E164(in, "")
		assert.ErrorIs(t, err, ErrInvalid, in)
	}
}

func TestInternationalAndIranianShapes(t *testing.T) {
	n, err := International("09121234567", "98")
	require.NoError(t, err)
	assert.Equal(t, "989121234567", n)

	n, err = IranLocalOrInternational("+989121234567")
	require.NoError(t, err)
	assert.Equal(t, "09121234567", n)

	n, err = IranLocalOrInternational("+44 7700 900123")
	require.NoError(t, err)
	assert.Equal(t, "00447700900123", n)

	_, err = IranLocalOrInternational("hello")
	assert.ErrorIs(t, err, ErrInvalid)
}
