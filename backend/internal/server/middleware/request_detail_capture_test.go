package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/topicsummary"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestDecodeCapturedBodyGzip(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"model":"gpt-test","input":"hello"}`))
	_ = writer.Close()

	decoded, truncated, err := decodeCapturedBody(compressed.Bytes(), "gzip", 256*1024)
	if err != nil {
		t.Fatalf("decodeCapturedBody() error = %v", err)
	}
	if truncated {
		t.Fatal("decodeCapturedBody() unexpectedly truncated")
	}
	if decoded != `{"model":"gpt-test","input":"hello"}` {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestRequestDetailCaptureSkipsInternalTopicSummaryRequest(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	details := service.NewRequestDetailService()
	events, unsubscribe := details.SubscribeLive(256)
	defer unsubscribe()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestDetailCapture(details))
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "test"})
		c.Next()
	})
	router.POST("/v1/responses", func(c *gin.Context) {
		_, _ = io.Copy(io.Discard, c.Request.Body)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"internal"}`))
	req.Header.Set(topicsummary.HeaderName, topicsummary.InternalToken("test-key"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	select {
	case event := <-events:
		t.Fatalf("internal topic request was captured: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDecodeCapturedBodyHonorsDecodedLimit(t *testing.T) {
	decoded, truncated, err := decodeCapturedBody([]byte(strings.Repeat("x", 20)), "identity", 10)
	if err != nil {
		t.Fatalf("decodeCapturedBody() error = %v", err)
	}
	if !truncated {
		t.Fatal("decodeCapturedBody() should report truncation")
	}
	if decoded != strings.Repeat("x", 10) {
		t.Fatalf("decoded length = %d, want 10", len(decoded))
	}
}
