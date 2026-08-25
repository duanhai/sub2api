package topicsummary

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

const HeaderName = "X-Sub2API-Topic-Summary-Internal"

var runtimeInternalToken atomic.Value

func InternalToken(apiKey string) string {
	if apiKey = strings.TrimSpace(apiKey); apiKey == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("sub2api-topic-summary:" + apiKey))
	return hex.EncodeToString(digest[:])
}

// SetInternalAPIKey updates the token used to identify internal distillation
// requests after panel settings change. The environment variable remains a
// startup fallback for older deployments during migration.
func SetInternalAPIKey(apiKey string) {
	runtimeInternalToken.Store(InternalToken(apiKey))
}

func IsInternalRequest(header http.Header) bool {
	value := strings.TrimSpace(header.Get(HeaderName))
	expected, _ := runtimeInternalToken.Load().(string)
	if expected == "" {
		expected = InternalToken(os.Getenv("OPENAI_API_KEY"))
	}
	return value != "" && expected != "" && subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}
