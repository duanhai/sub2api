package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyQueueFiveRequestsAndCrossInstanceCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rdb := testutil.NewRedisClient(t)
	cache := testutil.NewRedisConcurrencyCache(t, rdb)
	a, b := service.NewConcurrencyService(cache), service.NewConcurrencyService(cache)
	for _, svc := range []*service.ConcurrencyService{a, b} {
		svc.ConfigureAPIKeyQueues(map[string]config.APIKeyQueueConfig{"1": {MaxWaiting: 3, TimeoutSeconds: 3}})
	}
	first, err := a.AcquireAPIKeySlot(ctx, 1, 2, nil)
	require.NoError(t, err)
	defer first()
	second, err := b.AcquireAPIKeySlot(ctx, 1, 2, nil)
	require.NoError(t, err)
	defer second()
	type result struct {
		release func()
		err     error
	}
	results := make(chan result, 3)
	for i := 0; i < 3; i++ {
		go func() { release, err := b.AcquireAPIKeySlot(ctx, 1, 2, nil); results <- result{release, err} }()
	}
	require.Eventually(t, func() bool { return rdb.ZCard(ctx, "concurrency:api_key_wait:1").Val() == 3 }, time.Second, time.Millisecond)
	_, err = a.AcquireAPIKeySlot(ctx, 1, 2, nil)
	require.ErrorIs(t, err, service.ErrAPIKeyQueueFull)
	status, _, code, _ := concurrencyErrorResponse(err, "user")
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, "api_key_queue_full", code)
	// Other keys are not blocked by this queue.
	other, err := a.AcquireAPIKeySlot(ctx, 2, 1, nil)
	require.NoError(t, err)
	_, err = b.AcquireAPIKeySlot(ctx, 2, 1, nil)
	require.ErrorIs(t, err, service.ErrAPIKeyConcurrencyExceeded)
	other()
	first()
	for i := 0; i < 3; i++ {
		select {
		case got := <-results:
			require.NoError(t, got.err)
			counts, err := a.GetAPIKeyConcurrencyBatch(ctx, []int64{1})
			require.NoError(t, err)
			require.Equal(t, 2, counts[1], "completed waiting context must not release a live slot")
			got.release()
		case <-ctx.Done():
			t.Fatal("waiting request never admitted")
		}
	}
	second()
	require.Zero(t, rdb.ZCard(ctx, "concurrency:api_key_wait:1").Val())
}

func TestAPIKeyQueueTimeoutAndCancellationCleanup(t *testing.T) {
	ctx := context.Background()
	rdb := testutil.NewRedisClient(t)
	svc := service.NewConcurrencyService(testutil.NewRedisConcurrencyCache(t, rdb))
	svc.ConfigureAPIKeyQueues(map[string]config.APIKeyQueueConfig{"1": {MaxWaiting: 1, TimeoutSeconds: 1}})
	held, err := svc.AcquireAPIKeySlot(ctx, 1, 1, nil)
	require.NoError(t, err)
	defer held()
	_, err = svc.AcquireAPIKeySlot(ctx, 1, 1, nil)
	require.ErrorIs(t, err, service.ErrAPIKeyQueueTimeout)
	require.Zero(t, rdb.ZCard(ctx, "concurrency:api_key_wait:1").Val())
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		release, err := svc.AcquireAPIKeySlot(waitCtx, 1, 1, nil)
		if release != nil {
			release()
		}
		result <- err
	}()
	require.Eventually(t, func() bool { return rdb.ZCard(ctx, "concurrency:api_key_wait:1").Val() == 1 }, time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancel did not remove waiter")
	}
	require.Zero(t, rdb.ZCard(ctx, "concurrency:api_key_wait:1").Val())
	counts, err := svc.GetAPIKeyConcurrencyBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 1, counts[1], "canceling a waiter cannot remove another request's slot")
}

func TestAPIKeyQueueHTTPWaiterDoesNotHoldUserSlot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rdb := testutil.NewRedisClient(t)
	svc := service.NewConcurrencyService(testutil.NewRedisConcurrencyCache(t, rdb))
	svc.ConfigureAPIKeyQueues(map[string]config.APIKeyQueueConfig{"1": {MaxWaiting: 3, TimeoutSeconds: 3}})
	helper := NewConcurrencyHelper(svc, SSEPingFormatNone, time.Second)
	held, ok, err := helper.TryAcquireUserSlotForAPIKey(ctx, 9, 2, 1, 1, nil)
	require.NoError(t, err)
	require.True(t, ok)
	defer held()
	c, _ := newHelperTestContext(http.MethodPost, "/v1/responses")
	c.Request = c.Request.WithContext(ctx)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, ConcurrencyLimit: 1})
	result := make(chan error, 1)
	go func() {
		started := false
		release, err := helper.AcquireUserSlotWithWait(c, 9, 2, true, &started)
		if release != nil {
			release()
		}
		result <- err
	}()
	require.Eventually(t, func() bool { return rdb.ZCard(ctx, "concurrency:api_key_wait:1").Val() == 1 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, rdb.ZCard(ctx, "concurrency:user:9").Val())
	other, ok, err := helper.TryAcquireUserSlotForAPIKey(ctx, 9, 2, 2, 1, nil)
	require.NoError(t, err)
	require.True(t, ok)
	defer other()
	held()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("HTTP waiter did not resume")
	}
}

func TestAPIKeyQueueRedisFailureDoesNotBypassLimit(t *testing.T) {
	rdb := testutil.NewRedisClient(t)
	svc := service.NewConcurrencyService(testutil.NewRedisConcurrencyCache(t, rdb))
	svc.ConfigureAPIKeyQueues(map[string]config.APIKeyQueueConfig{"1": {MaxWaiting: 3, TimeoutSeconds: 1}})
	require.NoError(t, rdb.Close())
	release, err := svc.AcquireAPIKeySlot(context.Background(), 1, 2, nil)
	require.Error(t, err)
	require.Nil(t, release)
	status, _, _, _ := concurrencyErrorResponse(err, "user")
	require.Equal(t, http.StatusServiceUnavailable, status)
}

func TestAPIKeyQueueWebSocketDisconnectBeforeAdmission(t *testing.T) {
	var upstreamRequests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	rdb := testutil.NewRedisClient(t)
	svc := service.NewConcurrencyService(testutil.NewRedisConcurrencyCache(t, rdb))
	var held func()
	harness := newOpenAIWSPassthroughHandlerHarness(t, upstream.URL, func(h *OpenAIGatewayHandler, key *service.APIKey) {
		key.ConcurrencyLimit = 1
		svc.ConfigureAPIKeyQueues(map[string]config.APIKeyQueueConfig{strconv.FormatInt(key.ID, 10): {MaxWaiting: 3, TimeoutSeconds: 3}})
		h.concurrencyHelper = NewConcurrencyHelper(svc, SSEPingFormatNone, time.Second)
		h.apiKeyService = newConcurrencyTestAPIKeyService(key)
		var err error
		held, err = svc.AcquireAPIKeySlot(context.Background(), key.ID, 1, nil)
		require.NoError(t, err)
	})
	defer held()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, harness.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"queued"}`)))
	waitKey := "concurrency:api_key_wait:" + strconv.FormatInt(harness.apiKey.ID, 10)
	require.Eventually(t, func() bool { return rdb.ZCard(ctx, waitKey).Val() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, harness.clientConn.CloseNow())
	require.Eventually(t, func() bool { return rdb.ZCard(ctx, waitKey).Val() == 0 }, time.Second, time.Millisecond)
	require.Zero(t, upstreamRequests.Load(), "disconnected waiter must never reach upstream")
}

func TestAPIKeyQueueWebSocketSecondTurnWaitsAndResumes(t *testing.T) {
	var upstreamRequests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
			upstreamRequests.Add(1)
			if err := conn.Write(r.Context(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_queue","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`)); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	rdb := testutil.NewRedisClient(t)
	svc := service.NewConcurrencyService(testutil.NewRedisConcurrencyCache(t, rdb))
	harness := newOpenAIWSPassthroughHandlerHarness(t, upstream.URL, func(h *OpenAIGatewayHandler, key *service.APIKey) {
		key.ConcurrencyLimit = 1
		svc.ConfigureAPIKeyQueues(map[string]config.APIKeyQueueConfig{strconv.FormatInt(key.ID, 10): {MaxWaiting: 3, TimeoutSeconds: 3}})
		h.concurrencyHelper = NewConcurrencyHelper(svc, SSEPingFormatNone, time.Second)
		h.apiKeyService = newConcurrencyTestAPIKeyService(key)
	})
	// Ensure the downstream closes before the upstream test server is joined.
	defer func() { _ = harness.clientConn.CloseNow() }()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"hello"}`)
	require.NoError(t, harness.clientConn.Write(ctx, coderws.MessageText, payload))
	_, response, err := harness.clientConn.Read(ctx)
	require.NoError(t, err)
	require.Contains(t, string(response), "response.completed")
	require.Eventually(t, func() bool {
		counts, err := svc.GetAPIKeyConcurrencyBatch(ctx, []int64{harness.apiKey.ID})
		return err == nil && counts[harness.apiKey.ID] == 0
	}, time.Second, time.Millisecond)
	held, err := svc.AcquireAPIKeySlot(ctx, harness.apiKey.ID, 1, nil)
	require.NoError(t, err)
	defer held()
	require.NoError(t, harness.clientConn.Write(ctx, coderws.MessageText, payload))
	waitKey := "concurrency:api_key_wait:" + strconv.FormatInt(harness.apiKey.ID, 10)
	require.Eventually(t, func() bool { return rdb.ZCard(ctx, waitKey).Val() == 1 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, upstreamRequests.Load())
	held()
	_, response, err = harness.clientConn.Read(ctx)
	require.NoError(t, err)
	require.Contains(t, string(response), "response.completed")
	require.EqualValues(t, 2, upstreamRequests.Load())
	require.NoError(t, harness.clientConn.CloseNow())
	select {
	case <-harness.handlerDone:
	case <-ctx.Done():
		t.Fatal("handler did not stop")
	}
}
