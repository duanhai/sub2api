package service

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRequestDetailLiveSubscriptionPublishesWithoutRedis(t *testing.T) {
	svc := NewRequestDetailService()
	events, unsubscribe := svc.SubscribeLive(512)
	defer unsubscribe()

	if got := svc.LiveBodyLimit(); got != 512*1024 {
		t.Fatalf("LiveBodyLimit() = %d, want %d", got, 512*1024)
	}

	svc.PublishLive(RequestDetail{ID: "req-1", RequestBody: strings.Repeat("x", 600*1024), BodyState: RequestBodyCaptured})
	select {
	case event := <-events:
		if len(event.RequestBody) != 512*1024 {
			t.Fatalf("body length = %d, want %d", len(event.RequestBody), 512*1024)
		}
		if event.BodyState != RequestBodyTruncated {
			t.Fatalf("body state = %q, want %q", event.BodyState, RequestBodyTruncated)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}
}

func TestRequestDetailLiveSubscriptionDefaultsTo256KB(t *testing.T) {
	svc := NewRequestDetailService()
	_, unsubscribe := svc.SubscribeLive(999)
	if got := svc.LiveBodyLimit(); got != 256*1024 {
		t.Fatalf("LiveBodyLimit() = %d, want %d", got, 256*1024)
	}
	unsubscribe()
	if got := svc.LiveBodyLimit(); got != 0 {
		t.Fatalf("LiveBodyLimit() after unsubscribe = %d, want 0", got)
	}
}

func TestRequestDetailLiveSubscriptionAllows1MB(t *testing.T) {
	svc := NewRequestDetailService()
	_, unsubscribe := svc.SubscribeLive(1024)
	defer unsubscribe()

	if got := svc.LiveBodyLimit(); got != 1024*1024 {
		t.Fatalf("LiveBodyLimit() = %d, want %d", got, 1024*1024)
	}
}

func TestRequestDetailPersistentSinkWritesJSONWithoutLiveSubscriber(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "request-details-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	sink := newRequestDetailPersistentSink(file, 256*1024, "test-source", 4)
	svc := newRequestDetailService(sink)
	if got := svc.CaptureBodyLimit(); got != 256*1024 {
		t.Fatalf("CaptureBodyLimit() = %d, want %d", got, 256*1024)
	}
	svc.Publish(RequestDetail{ID: "req-1", RequestBody: `{"input":"hello"}`, BodyState: RequestBodyCaptured})

	var detail RequestDetail
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(file.Name())
		if readErr == nil && len(data) > 0 && json.Unmarshal(data, &detail) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if detail.ID != "req-1" || detail.Source != "test-source" {
		t.Fatalf("persisted detail = %+v", detail)
	}
}

type blockingRequestDetailWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockingRequestDetailWriter) Write(p []byte) (int, error) {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.release
	return len(p), nil
}

func TestRequestDetailPersistentQueueNeverBlocksPublisher(t *testing.T) {
	writer := &blockingRequestDetailWriter{started: make(chan struct{}, 1), release: make(chan struct{})}
	sink := newRequestDetailPersistentSink(writer, 256*1024, "", 1)
	svc := newRequestDetailService(sink)
	svc.Publish(RequestDetail{ID: "first"})
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	svc.Publish(RequestDetail{ID: "queued"})

	started := time.Now()
	svc.Publish(RequestDetail{ID: "dropped"})
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("Publish() blocked for %v", elapsed)
	}
	if sink.dropped.Load() != 1 {
		t.Fatalf("dropped = %d, want 1", sink.dropped.Load())
	}
	close(writer.release)
}

func TestRequestDetailPersistentBodyLimitDefaultsAndValidation(t *testing.T) {
	for _, value := range []string{"", "invalid", "1024"} {
		if got := requestDetailBodyLimitFromEnv(value); got != 256*1024 {
			t.Fatalf("requestDetailBodyLimitFromEnv(%q) = %d", value, got)
		}
	}
	if got := requestDetailBodyLimitFromEnv("512"); got != 512*1024 {
		t.Fatalf("requestDetailBodyLimitFromEnv(512) = %d", got)
	}
}

func TestRequestDetailLogModeDefaultsToRaw(t *testing.T) {
	for _, value := range []string{"", "invalid", "RAW"} {
		if got := requestDetailLogModeFromEnv(value); got != requestDetailLogModeRaw {
			t.Fatalf("requestDetailLogModeFromEnv(%q) = %q, want raw", value, got)
		}
	}
	if got := requestDetailLogModeFromEnv(" dual "); got != requestDetailLogModeDual {
		t.Fatalf("dual mode = %q", got)
	}
	if got := requestDetailLogModeFromEnv("STRUCTURED"); got != requestDetailLogModeStructured {
		t.Fatalf("structured mode = %q", got)
	}
}

func TestRequestDetailStructuredSinkOmitsRawBody(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "request-details-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	sink := newRequestDetailPersistentSink(file, 256*1024, "test-source", 4)
	sink.mode = requestDetailLogModeStructured
	svc := newRequestDetailService(sink)
	svc.Publish(RequestDetail{
		ID: "req-structured", Path: "/v1/responses", StatusCode: 200,
		RequestBody: `{"input":"raw"}`, BodyState: RequestBodyCaptured,
		RequestSize: 98765, ConversationVersion: 1, ConversationExtractState: RequestConversationExtractCaptured,
		CurrentUserText: "current user", PreviousAssistantText: "previous assistant",
	})

	var detail RequestDetail
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(file.Name())
		if readErr == nil && len(data) > 0 && json.Unmarshal(data, &detail) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if detail.RequestBody != "" || detail.BodyState != RequestBodyStructured {
		t.Fatalf("structured detail retained raw body: %+v", detail)
	}
	if detail.CurrentUserText != "current user" || detail.PreviousAssistantText != "previous assistant" {
		t.Fatalf("structured detail lost conversation: %+v", detail)
	}
	if detail.RequestSize != 98765 {
		t.Fatalf("structured detail lost request size: %+v", detail)
	}
}

func TestShouldOmitStructuredRequestBody(t *testing.T) {
	tests := []struct {
		name   string
		detail RequestDetail
		want   bool
	}{
		{
			name: "captured responses success",
			detail: RequestDetail{Path: "/responses", StatusCode: 200,
				ConversationExtractState: RequestConversationExtractCaptured},
			want: true,
		},
		{
			name: "captured v1 responses redirect",
			detail: RequestDetail{Path: "/v1/responses", StatusCode: 307,
				ConversationExtractState: RequestConversationExtractCaptured},
			want: true,
		},
		{
			name: "responses HTTP error",
			detail: RequestDetail{Path: "/responses", StatusCode: 400,
				ConversationExtractState: RequestConversationExtractCaptured},
		},
		{
			name: "responses no new user",
			detail: RequestDetail{Path: "/responses", StatusCode: 200,
				ConversationExtractState: RequestConversationExtractNoNewUser},
		},
		{
			name: "unsupported messages route",
			detail: RequestDetail{Path: "/v1/messages", StatusCode: 200,
				ConversationExtractState: RequestConversationExtractCaptured},
		},
		{
			name: "unsupported chat route",
			detail: RequestDetail{Path: "/v1/chat/completions", StatusCode: 200,
				ConversationExtractState: RequestConversationExtractCaptured},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldOmitStructuredRequestBody(test.detail); got != test.want {
				t.Fatalf("shouldOmitStructuredRequestBody() = %v, want %v", got, test.want)
			}
		})
	}
}
