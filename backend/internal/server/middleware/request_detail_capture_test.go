package middleware

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestRequestDetailCapturePersistsWithoutLiveSubscriber(t *testing.T) {
	logPath := t.TempDir() + "/request-details.jsonl"
	t.Setenv("SUB2API_REQUEST_DETAIL_LOG_PATH", logPath)
	t.Setenv("SUB2API_REQUEST_DETAIL_LOG_SOURCE", "test-server")
	details := service.NewRequestDetailService()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger())
	router.Use(RequestDetailCapture(details))
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "test"})
		c.Next()
	})
	router.POST("/v1/responses", func(c *gin.Context) {
		_, _ = io.Copy(io.Discard, c.Request.Body)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var detail service.RequestDetail
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil && len(data) > 0 && json.Unmarshal(data, &detail) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if detail.APIKeyID != 7 || detail.Model != "gpt-test" || detail.Source != "test-server" {
		t.Fatalf("persisted detail = %+v", detail)
	}
	if detail.LocalID == "" || detail.RequestBody == "" {
		t.Fatalf("missing correlation or request body: %+v", detail)
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

func TestRequestDetailCaptureIncludesBoundConversationObservation(t *testing.T) {
	details := service.NewRequestDetailService()
	events, unsubscribe := details.SubscribeLive(256)
	defer unsubscribe()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestLogger())
	router.Use(RequestDetailCapture(details))
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "test"})
		c.Next()
	})
	router.POST("/v1/responses", func(c *gin.Context) {
		_, _ = io.Copy(io.Discard, c.Request.Body)
		service.EnableRequestConversationCapture(c)
		service.BindRequestConversationObservation(c, "current user", "previous assistant")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hello"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	select {
	case detail := <-events:
		if detail.ConversationExtractState != service.RequestConversationExtractCaptured {
			t.Fatalf("conversation extract state missing: %+v", detail)
		}
		if detail.CurrentUserText != "current user" || detail.PreviousAssistantText != "previous assistant" {
			t.Fatalf("structured conversation missing: %+v", detail)
		}
		if detail.AssistantSource != "client_request_history" || detail.AssistantLag != 1 {
			t.Fatalf("assistant provenance missing: %+v", detail)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for request detail")
	}
}
