package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRequestDetailLiveSubscriptionPublishesWithoutRedis(t *testing.T) {
	svc := NewRequestDetailService(nil, nil)
	events, unsubscribe := svc.SubscribeLive(512)
	defer unsubscribe()

	if got := svc.LiveBodyLimit(); got != 512*1024 {
		t.Fatalf("LiveBodyLimit() = %d, want %d", got, 512*1024)
	}

	svc.Capture(context.Background(), RequestDetail{ID: "req-1", RequestBody: strings.Repeat("x", 600*1024)})
	select {
	case event := <-events:
		if len(event.RequestBody) != 512*1024 {
			t.Fatalf("body length = %d, want %d", len(event.RequestBody), 512*1024)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live event")
	}
}

func TestRequestDetailLiveSubscriptionDefaultsTo256KB(t *testing.T) {
	svc := NewRequestDetailService(nil, nil)
	_, unsubscribe := svc.SubscribeLive(999)
	if got := svc.LiveBodyLimit(); got != 256*1024 {
		t.Fatalf("LiveBodyLimit() = %d, want %d", got, 256*1024)
	}
	unsubscribe()
	if got := svc.LiveBodyLimit(); got != 0 {
		t.Fatalf("LiveBodyLimit() after unsubscribe = %d, want 0", got)
	}
}
