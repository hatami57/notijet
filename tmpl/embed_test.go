package tmpl

import (
	"embed"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// go:embed skips files and directories starting with "_" or "."; partials
// must survive embedding with a plain directory pattern.
//
//go:embed testdata/embedded
var embedded embed.FS

func TestPartialsSurviveEmbed(t *testing.T) {
	s, err := ParseFS(embedded, "testdata/embedded")
	require.NoError(t, err)
	r, err := s.Render("hello", map[string]string{"Name": "Ali"})
	require.NoError(t, err)
	assert.Equal(t, "Hello Ali -- Acme", r.Text)
}
