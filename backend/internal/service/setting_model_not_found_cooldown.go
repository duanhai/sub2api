package service

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sync/singleflight"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type cachedModelNotFoundCooldownSetting struct {
	value     string // TrimSpace 后的后台原始值；"" 表示后台未设置
	expiresAt int64
}

const modelNotFoundCooldownSettingCacheTTL = 5 * time.Second
const upstreamModelNotFoundMaxCooldownSec = 86400

// getModelNotFoundCooldownRawSetting 读取上游模型不存在冷却设置的原始值。
// 返回 (value, true) 表示读到了后台值（可能为 ""，即后台未设置，调用方应回退 yaml/env）；
// 返回 (_, false) 表示本次无法判定（ctx 结束、无 repo、DB 出错且无缓存），调用方回退 fallback。
// 进程内缓存 5 秒，singleflight 合并并发回源；DB 出错时沿用上一次缓存值。
func (s *SettingService) getModelNotFoundCooldownRawSetting(ctx context.Context, cache *atomic.Value, sf *singleflight.Group, key string) (string, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return "", false
	}
	if s == nil || s.settingRepo == nil {
		return "", false
	}
	if cached, ok := cache.Load().(*cachedModelNotFoundCooldownSetting); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.value, true
		}
	}
	resultCh := sf.DoChan(key, func() (any, error) {
		if cached, ok := cache.Load().(*cachedModelNotFoundCooldownSetting); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.value, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, key)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			if cached, ok := cache.Load().(*cachedModelNotFoundCooldownSetting); ok && cached != nil {
				return cached.value, nil
			}
			return nil, err
		}
		if err != nil {
			value = ""
		}
		value = strings.TrimSpace(value)
		cache.Store(&cachedModelNotFoundCooldownSetting{
			value:     value,
			expiresAt: time.Now().Add(modelNotFoundCooldownSettingCacheTTL).UnixNano(),
		})
		return value, nil
	})
	select {
	case <-ctx.Done():
		return "", false
	case result := <-resultCh:
		if v, ok := result.Val.(string); ok && result.Err == nil {
			return v, true
		}
		return "", false
	}
}

func invalidateModelNotFoundCooldownSettingCache(cache *atomic.Value, sf *singleflight.Group, key string) {
	sf.Forget(key)
	cache.Store(&cachedModelNotFoundCooldownSetting{expiresAt: 0})
}

func (s *SettingService) getModelNotFoundCooldownIntSetting(ctx context.Context, cache *atomic.Value, sf *singleflight.Group, key string, fallback int, validate func(int) error) int {
	if s == nil {
		return fallback
	}
	raw, ok := s.getModelNotFoundCooldownRawSetting(ctx, cache, sf, key)
	if !ok || raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || (validate != nil && validate(n) != nil) {
		return fallback
	}
	return n
}

// GetUpstreamModelNotFoundCooldownSeconds 返回上游「模型不存在」冷却秒数（0 表示不冷却）。
func (s *SettingService) GetUpstreamModelNotFoundCooldownSeconds(ctx context.Context, fallback int) int {
	if s == nil {
		return fallback
	}
	return s.getModelNotFoundCooldownIntSetting(ctx, &s.upstreamModelNotFoundCooldownCache, &s.upstreamModelNotFoundCooldownSF, SettingKeyUpstreamModelNotFoundCooldownSeconds, fallback, ValidateUpstreamModelNotFoundCooldownSeconds)
}

func (s *SettingService) InvalidateUpstreamModelNotFoundCooldownCache() {
	if s == nil {
		return
	}
	invalidateModelNotFoundCooldownSettingCache(&s.upstreamModelNotFoundCooldownCache, &s.upstreamModelNotFoundCooldownSF, SettingKeyUpstreamModelNotFoundCooldownSeconds)
}

func ValidateUpstreamModelNotFoundCooldownSeconds(n int) error {
	if n < 0 || n > upstreamModelNotFoundMaxCooldownSec {
		return fmt.Errorf("model-not-found cooldown must be between 0 and %d seconds", upstreamModelNotFoundMaxCooldownSec)
	}
	return nil
}
