package topicsummary

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsInternalRequestUsesRuntimeAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	SetInternalAPIKey("panel-key")
	t.Cleanup(func() { SetInternalAPIKey("") })
	header := make(http.Header)
	header.Set(HeaderName, InternalToken("panel-key"))
	require.True(t, IsInternalRequest(header))

	header.Set(HeaderName, InternalToken("different-key"))
	require.False(t, IsInternalRequest(header))
}
