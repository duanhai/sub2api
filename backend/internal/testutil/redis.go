package testutil

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// NewRedisClient returns a real Redis client backed by an in-memory test server.
func NewRedisClient(t *testing.T) *redis.Client {
	t.Helper()

	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })

	return redisClient
}

// NewRedisGatewayCache returns a real Redis-backed gateway cache for tests.
func NewRedisGatewayCache(t *testing.T) service.GatewayCache {
	t.Helper()

	return repository.NewGatewayCache(NewRedisClient(t))
}

// NewRedisConcurrencyCache exercises the production Lua scripts without Docker.
func NewRedisConcurrencyCache(t *testing.T, clients ...*redis.Client) service.ConcurrencyCache {
	t.Helper()
	if len(clients) > 0 {
		return repository.NewConcurrencyCache(clients[0], 1, 60)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return repository.NewConcurrencyCache(client, 1, 60)
}
