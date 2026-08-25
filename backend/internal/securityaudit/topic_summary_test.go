package securityaudit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/topicsummary"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestTopicSummaryServiceDistillsOncePerSessionAndExpires(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })

	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, topicsummary.InternalToken("test-key"), r.Header.Get(TopicSummaryInternalHeader))
		var request map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "gpt-5.6-luna", request["model"])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{map[string]any{
			"content": []any{map[string]any{"text": `{"title":"Docker 更新","category":"运维部署","summary":"讨论 Docker Compose 更新与停机风险"}`}},
		}}})
	}))
	t.Cleanup(endpoint.Close)

	service := newTopicSummaryService(redisClient, topicSummaryConfig{
		APIKey: "test-key", ResponsesURL: endpoint.URL, Model: "gpt-5.6-luna",
		InternalToken: topicsummary.InternalToken("test-key"),
	})
	fixedNow := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedNow }
	request := Request{
		RequestID: "req-1", APIKeyID: 7, SessionID: "session-1", Protocol: "openai_responses",
		Body: []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"Docker 如何不停机更新？"}]}]}`),
	}
	service.Observe(request)
	request.RequestID = "req-2"
	service.Observe(request)

	require.Eventually(t, func() bool {
		summaries, err := service.GetMany(t.Context(), []string{"req-1", "req-2"})
		return err == nil && len(summaries) == 2
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, int32(1), calls.Load())
	summaries, err := service.GetMany(t.Context(), []string{"req-1", "req-2"})
	require.NoError(t, err)
	require.Equal(t, "Docker 更新", summaries["req-1"].Title)
	require.Equal(t, "completed", summaries["req-2"].Status)
	usageSummaries, err := service.GetForUsage(t.Context(), []TopicSummaryUsageLookup{{
		RequestID: "client:different-request-id", APIKeyID: 7, SessionID: "session-1", CreatedAt: fixedNow,
	}})
	require.NoError(t, err)
	require.Equal(t, "Docker 更新", usageSummaries["client:different-request-id"].Title)

	redisServer.FastForward(topicSummaryTTL + time.Second)
	summaries, err = service.GetMany(t.Context(), []string{"req-1", "req-2"})
	require.NoError(t, err)
	require.Empty(t, summaries)
}

func TestTopicSummaryServiceSkipsInternalDistillationRequest(t *testing.T) {
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })

	service := newTopicSummaryService(redisClient, topicSummaryConfig{
		APIKey: "test-key", ResponsesURL: "https://example.invalid/v1/responses", Model: "gpt-5.6-luna",
		InternalToken: topicsummary.InternalToken("test-key"),
	})
	service.Observe(Request{
		RequestID: "internal", APIKeyID: 7, Protocol: "openai_responses",
		Body: []byte(`{"input":"summarize me"}`), TopicInternalToken: topicsummary.InternalToken("test-key"),
	})

	require.Empty(t, redisServer.Keys())
	require.Empty(t, service.sessions)
}

func TestTopicSummaryResponsesURL(t *testing.T) {
	require.Equal(t, "https://example.com/v1/responses", topicSummaryResponsesURL("https://example.com"))
	require.Equal(t, "https://example.com/v1/responses", topicSummaryResponsesURL("https://example.com/v1/"))
	require.Empty(t, topicSummaryResponsesURL(""))
}
