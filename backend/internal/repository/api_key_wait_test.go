package repository

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyWaitCapacityAndExpiry(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewConcurrencyCache(client, 1, 60).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	var admitted, failures atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := cache.AddAPIKeyWaiter(ctx, 1, strconv.Itoa(i), 3, time.Second)
			if err != nil {
				failures.Add(1)
			}
			if ok {
				admitted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	require.EqualValues(t, 3, admitted.Load())
	// An independent key and an unrelated cleanup cannot affect capacity.
	ok, err := cache.AddAPIKeyWaiter(ctx, 2, "other", 1, 10*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, cache.RemoveAPIKeyWaiter(ctx, 1, "unknown"))
	require.EqualValues(t, 3, client.ZCard(ctx, apiKeyWaitKey(1)).Val())
	// Model a crashed process: no cleanup occurs, leases expire on their own.
	r.FastForward(2 * time.Second)
	ok, err = cache.AddAPIKeyWaiter(ctx, 1, "replacement", 3, time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 1, client.ZCard(ctx, apiKeyWaitKey(1)).Val())
	require.EqualValues(t, 1, client.ZCard(ctx, apiKeyWaitKey(2)).Val())
}
