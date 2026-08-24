package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const requestDetailSettingsKey = "request_detail_config"
const requestDetailPrefix = "sub2api:request_detail:"

// RequestDetailConfig deliberately defaults off: request metadata can be useful
// for debugging, but retaining prompt-derived data must be an explicit choice.
type RequestDetailConfig struct {
	Enabled          bool `json:"enabled"`
	BodyPreview      bool `json:"body_preview"`
	RetentionHours   int  `json:"retention_hours"`
	RetentionMinutes int  `json:"retention_minutes"`
}

type RequestDetail struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Model       string    `json:"model,omitempty"`
	StatusCode  int       `json:"status_code"`
	DurationMs  int64     `json:"duration_ms"`
	UserID      int64     `json:"user_id,omitempty"`
	APIKeyID    int64     `json:"api_key_id,omitempty"`
	APIKeyName  string    `json:"api_key_name,omitempty"`
	UserEmail   string    `json:"user_email,omitempty"`
	Username    string    `json:"username,omitempty"`
	GroupID     *int64    `json:"group_id,omitempty"`
	RequestBody string    `json:"request_body,omitempty"`
}

type RequestDetailService struct {
	redis           *redis.Client
	settings        SettingRepository
	configMu        sync.Mutex
	cachedConfig    RequestDetailConfig
	cachedConfigAt  time.Time
	liveMu          sync.RWMutex
	liveSubscribers map[chan RequestDetail]int
}

type RequestDetailQuery struct {
	Page, PageSize                    int
	Model, StatusCode, APIKeyID, User string
}
type RequestDetailPage struct {
	Items    []RequestDetail `json:"items"`
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Pages    int             `json:"pages"`
}

func NewRequestDetailService(redisClient *redis.Client, settings SettingRepository) *RequestDetailService {
	return &RequestDetailService{redis: redisClient, settings: settings, liveSubscribers: make(map[chan RequestDetail]int)}
}

func (s *RequestDetailService) LiveBodyLimit() int {
	if s == nil {
		return 0
	}
	s.liveMu.RLock()
	defer s.liveMu.RUnlock()
	limit := 0
	for _, candidate := range s.liveSubscribers {
		if candidate > limit {
			limit = candidate
		}
	}
	return limit
}

func (s *RequestDetailService) SubscribeLive(bodyLimitKB int) (<-chan RequestDetail, func()) {
	if bodyLimitKB != 512 {
		bodyLimitKB = 256
	}
	ch := make(chan RequestDetail, 64)
	s.liveMu.Lock()
	s.liveSubscribers[ch] = bodyLimitKB * 1024
	s.liveMu.Unlock()
	return ch, func() {
		s.liveMu.Lock()
		if _, ok := s.liveSubscribers[ch]; ok {
			delete(s.liveSubscribers, ch)
			close(ch)
		}
		s.liveMu.Unlock()
	}
}

func (s *RequestDetailService) publishLive(detail RequestDetail) {
	s.liveMu.RLock()
	defer s.liveMu.RUnlock()
	for ch, limit := range s.liveSubscribers {
		item := detail
		if len(item.RequestBody) > limit {
			item.RequestBody = item.RequestBody[:limit]
		}
		select {
		case ch <- item:
		default:
		}
	}
}

func defaultRequestDetailConfig() RequestDetailConfig {
	return RequestDetailConfig{RetentionMinutes: 30}
}

func (s *RequestDetailService) Config(ctx context.Context) (RequestDetailConfig, error) {
	cfg := defaultRequestDetailConfig()
	if s == nil || s.settings == nil {
		return cfg, nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if !s.cachedConfigAt.IsZero() && time.Since(s.cachedConfigAt) < 5*time.Second {
		return s.cachedConfig, nil
	}
	raw, err := s.settings.GetValue(ctx, requestDetailSettingsKey)
	if err == ErrSettingNotFound {
		s.cachedConfig = cfg
		s.cachedConfigAt = time.Now()
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, err
	}
	if cfg.RetentionMinutes == 0 && cfg.RetentionHours > 0 {
		if cfg.RetentionHours == 1 {
			cfg.RetentionMinutes = 30
		} else {
			cfg.RetentionMinutes = cfg.RetentionHours * 60
		}
	}
	if cfg.RetentionMinutes < 5 {
		cfg.RetentionMinutes = 5
	}
	if cfg.RetentionMinutes > 720 {
		cfg.RetentionMinutes = 720
	}
	s.cachedConfig = cfg
	s.cachedConfigAt = time.Now()
	return cfg, nil
}

func (s *RequestDetailService) UpdateConfig(ctx context.Context, cfg RequestDetailConfig) (RequestDetailConfig, error) {
	if cfg.RetentionMinutes < 5 || cfg.RetentionMinutes > 720 {
		return cfg, fmt.Errorf("retention_minutes must be between 5 and 720")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if s == nil || s.settings == nil {
		return cfg, fmt.Errorf("request detail settings unavailable")
	}
	if err := s.settings.Set(ctx, requestDetailSettingsKey, string(raw)); err != nil {
		return cfg, err
	}
	s.configMu.Lock()
	s.cachedConfig = cfg
	s.cachedConfigAt = time.Now()
	s.configMu.Unlock()
	return cfg, nil
}

func (s *RequestDetailService) Capture(ctx context.Context, detail RequestDetail) {
	if s == nil {
		return
	}
	s.publishLive(detail)
	if s.redis == nil {
		return
	}
	cfg, err := s.Config(ctx)
	if err != nil || !cfg.Enabled {
		return
	}
	history := detail
	history.RequestBody = ""
	raw, err := json.Marshal(history)
	if err != nil {
		return
	}
	go func() {
		_ = s.redis.Set(context.Background(), requestDetailPrefix+detail.ID, raw, time.Duration(cfg.RetentionMinutes)*time.Minute).Err()
	}()
}

func (s *RequestDetailService) List(ctx context.Context, query RequestDetailQuery) (*RequestDetailPage, error) {
	if s == nil || s.redis == nil {
		return &RequestDetailPage{Items: []RequestDetail{}, Page: 1, PageSize: 50}, nil
	}
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 50
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	keys, err := s.redis.Keys(ctx, requestDetailPrefix+"*").Result()
	if err != nil {
		return nil, err
	}
	values, err := s.redis.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	items := make([]RequestDetail, 0, len(values))
	for _, value := range values {
		if raw, ok := value.(string); ok {
			var item RequestDetail
			if json.Unmarshal([]byte(raw), &item) == nil {
				items = append(items, item)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	filtered := items[:0]
	for _, item := range items {
		if query.Model != "" && !strings.Contains(strings.ToLower(item.Model), strings.ToLower(query.Model)) {
			continue
		}
		if query.StatusCode != "" && fmt.Sprint(item.StatusCode) != query.StatusCode {
			continue
		}
		if query.APIKeyID != "" && fmt.Sprint(item.APIKeyID) != query.APIKeyID {
			continue
		}
		if query.User != "" && !strings.Contains(strings.ToLower(item.UserEmail+" "+item.Username), strings.ToLower(query.User)) {
			continue
		}
		filtered = append(filtered, item)
	}
	total := len(filtered)
	start := (query.Page - 1) * query.PageSize
	if start > total {
		start = total
	}
	end := start + query.PageSize
	if end > total {
		end = total
	}
	pages := (total + query.PageSize - 1) / query.PageSize
	return &RequestDetailPage{Items: filtered[start:end], Total: total, Page: query.Page, PageSize: query.PageSize, Pages: pages}, nil
}

func RequestDetailModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}
