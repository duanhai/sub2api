package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Each waiter has its own deadline. A busy key cannot keep crashed waiters
// alive by refreshing a shared counter TTL. Use Redis time across instances.
var addAPIKeyWaiterScript = redis.NewScript(`
redis.replicate_commands()
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[1]) then return 0 end
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[3]), ARGV[2])
local last = redis.call('ZREVRANGE', KEYS[1], 0, 0, 'WITHSCORES')
redis.call('PEXPIRE', KEYS[1], math.ceil(tonumber(last[2]) - now))
return 1
`)

func apiKeyWaitKey(id int64) string { return fmt.Sprintf("concurrency:api_key_wait:%d", id) }

func (c *concurrencyCache) AddAPIKeyWaiter(ctx context.Context, id int64, requestID string, maxWaiting int, ttl time.Duration) (bool, error) {
	n, err := addAPIKeyWaiterScript.Run(ctx, c.rdb, []string{apiKeyWaitKey(id)}, maxWaiting, requestID, ttl.Milliseconds()).Int()
	return n == 1, err
}

func (c *concurrencyCache) RemoveAPIKeyWaiter(ctx context.Context, id int64, requestID string) error {
	return c.rdb.ZRem(ctx, apiKeyWaitKey(id), requestID).Err()
}
