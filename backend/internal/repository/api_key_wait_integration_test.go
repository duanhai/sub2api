//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyWaitRealRedisPrunesExpiredMembers(t *testing.T) {
	rdb := testRedis(t)
	cache := &concurrencyCache{rdb: rdb}
	ctx := context.Background()
	// A live member keeps the key alive while a crashed waiter's own deadline expires.
	now, err := rdb.Time(ctx).Result()
	require.NoError(t, err)
	require.NoError(t, rdb.ZAdd(ctx, apiKeyWaitKey(1), redis.Z{Score: float64(now.Add(-time.Second).UnixMilli()), Member: "crashed"}).Err())
	for _, id := range []string{"live", "replacement"} {
		ok, err := cache.AddAPIKeyWaiter(ctx, 1, id, 2, time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, err := cache.AddAPIKeyWaiter(ctx, 1, "overflow", 2, time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, cache.RemoveAPIKeyWaiter(ctx, 1, "replacement"))
	members, err := rdb.ZRange(ctx, apiKeyWaitKey(1), 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"live"}, members)
	ttl, err := rdb.PTTL(ctx, apiKeyWaitKey(1)).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	require.LessOrEqual(t, ttl, time.Minute)
}
