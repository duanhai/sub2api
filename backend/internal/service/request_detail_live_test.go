package service

import (
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
