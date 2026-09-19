package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

var (
	ErrAPIKeyQueueFull    = fmt.Errorf("API key wait queue full: %w", ErrAPIKeyConcurrencyExceeded)
	ErrAPIKeyQueueTimeout = fmt.Errorf("API key wait timeout: %w", ErrAPIKeyConcurrencyExceeded)
)

type APIKeyWaitCache interface {
	AddAPIKeyWaiter(context.Context, int64, string, int, time.Duration) (bool, error)
	RemoveAPIKeyWaiter(context.Context, int64, string) error
}

type apiKeyQueuePolicy struct {
	maxWaiting int
	timeout    time.Duration
}

// ConfigureAPIKeyQueues is startup-only; callers must not modify policy while
// requests are running. Policies use database key IDs, never secret key values.
func (s *ConcurrencyService) ConfigureAPIKeyQueues(policies map[string]config.APIKeyQueueConfig) {
	s.apiKeyQueues = make(map[int64]apiKeyQueuePolicy, len(policies))
	for key, p := range policies {
		id, err := strconv.ParseInt(key, 10, 64)
		if err == nil && id > 0 && p.MaxWaiting > 0 && p.MaxWaiting <= 100 && p.TimeoutSeconds > 0 && p.TimeoutSeconds <= 60 {
			s.apiKeyQueues[id] = apiKeyQueuePolicy{p.MaxWaiting, time.Duration(p.TimeoutSeconds) * time.Second}
		}
	}
}

func (s *ConcurrencyService) APIKeyQueueEnabled(id int64) bool {
	return s != nil && s.apiKeyQueues[id].maxWaiting > 0
}

func (s *ConcurrencyService) acquireAPIKeySlotWithQueue(ctx context.Context, id int64, limit int, onLeaseLost func()) (func(), error) {
	if limit <= 0 || !s.APIKeyQueueEnabled(id) {
		return s.tryAcquireAPIKeySlot(ctx, ctx, id, limit, onLeaseLost)
	}
	p := s.apiKeyQueues[id]
	waitCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	// Only admission observes socket disconnection. An acquired slot retains
	// the owner's original lifetime so upstream usage/drain can finish.
	if reader := openAIWSReaderFromContext(ctx); reader != nil {
		stop := context.AfterFunc(reader.peer, cancel)
		defer stop()
		if reader.peer.Err() != nil {
			return nil, context.Canceled
		}
	}
	attempt := func() (func(), error) {
		release, err := s.tryAcquireAPIKeySlot(ctx, waitCtx, id, limit, onLeaseLost)
		if reader := openAIWSReaderFromContext(ctx); reader != nil && reader.peer.Err() != nil {
			cancel()
		}
		if waitCtx.Err() != nil {
			if release != nil {
				release()
			}
			return nil, waitCtx.Err()
		}
		return release, err
	}
	release, err := attempt()
	if !errors.Is(err, ErrAPIKeyConcurrencyExceeded) {
		return release, s.apiKeyWaitError(ctx, waitCtx, err)
	}
	cache, ok := s.cache.(APIKeyWaitCache)
	if !ok {
		return nil, errors.New("API key wait cache is unavailable")
	}
	requestID := generateRequestID()
	// Cleanup even on uncertain Redis results (e.g. response lost after commit).
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), apiKeySlotTrackTimeout)
		defer cleanupCancel()
		if err := cache.RemoveAPIKeyWaiter(cleanupCtx, id, requestID); err != nil {
			logger.LegacyPrintf("service.concurrency", "API key waiter cleanup failed; deadline will expire it: key_id=%d error=%v", id, err)
		}
	}()
	opCtx, opCancel := context.WithTimeout(waitCtx, apiKeySlotTrackTimeout)
	added, err := cache.AddAPIKeyWaiter(opCtx, id, requestID, p.maxWaiting, p.timeout+5*time.Second)
	opCancel()
	if err != nil {
		return nil, s.apiKeyWaitError(ctx, waitCtx, err)
	}
	if !added {
		return nil, ErrAPIKeyQueueFull
	}
	// ponytail: bounded polling, not FIFO; use atomic ordered promotion only if
	// strict fairness becomes a requirement. New arrivals may acquire first.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		release, err = attempt()
		if !errors.Is(err, ErrAPIKeyConcurrencyExceeded) {
			return release, s.apiKeyWaitError(ctx, waitCtx, err)
		}
		select {
		case <-waitCtx.Done():
			return nil, s.apiKeyWaitError(ctx, waitCtx, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (s *ConcurrencyService) apiKeyWaitError(ctx, waitCtx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if reader := openAIWSReaderFromContext(ctx); reader != nil && reader.peer.Err() != nil {
		return context.Canceled
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return ErrAPIKeyQueueTimeout
	}
	return err
}
