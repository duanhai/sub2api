package topicsummary

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
)

const HeaderName = "X-Sub2API-Topic-Summary-Internal"

func InternalToken(apiKey string) string {
	if apiKey = strings.TrimSpace(apiKey); apiKey == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("sub2api-topic-summary:" + apiKey))
	return hex.EncodeToString(digest[:])
}

func IsInternalRequest(header http.Header) bool {
	value := strings.TrimSpace(header.Get(HeaderName))
	expected := InternalToken(os.Getenv("OPENAI_API_KEY"))
	return value != "" && expected != "" && subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}
