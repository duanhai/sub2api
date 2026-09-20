package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const settingKeyAPIKeyQueue = "api_key_queue"

type APIKeyQueueSettings struct {
	Enabled        bool `json:"enabled"`
	MaxWaiting     int  `json:"max_waiting"`
	TimeoutSeconds int  `json:"timeout_seconds"`
}

func (s APIKeyQueueSettings) Validate() error {
	if s.MaxWaiting < 1 || s.MaxWaiting > 100 || s.TimeoutSeconds < 1 || s.TimeoutSeconds > 60 {
		return errors.New("max_waiting must be 1..100 and timeout_seconds must be 1..60")
	}
	return nil
}

type cachedAPIKeyQueueSettings struct {
	value     *APIKeyQueueSettings // nil means no panel override; retain legacy YAML behavior
	expiresAt time.Time
}

// GetAPIKeyQueueSettings reads the persisted panel setting. Missing is distinct
// from disabled so upgrading does not silently disable existing YAML policies.
func (s *SettingService) GetAPIKeyQueueSettings(ctx context.Context) (*APIKeyQueueSettings, error) {
	raw, err := s.settingRepo.GetValue(ctx, settingKeyAPIKeyQueue)
	if errors.Is(err, ErrSettingNotFound) || (err == nil && raw == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var value APIKeyQueueSettings
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	return &value, value.Validate()
}

func (s *SettingService) SetAPIKeyQueueSettings(ctx context.Context, value APIKeyQueueSettings) error {
	if err := value.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.apiKeyQueueMu.Lock()
	defer s.apiKeyQueueMu.Unlock()
	if err := s.settingRepo.Set(ctx, settingKeyAPIKeyQueue, string(raw)); err != nil {
		return err
	}
	s.apiKeyQueueCache.Store(&cachedAPIKeyQueueSettings{value: &value, expiresAt: time.Now().Add(5 * time.Second)})
	return nil
}

func (s *SettingService) apiKeyQueueRuntimeSettings(ctx context.Context) *APIKeyQueueSettings {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if cached := s.apiKeyQueueCache.Load(); cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.value
	}
	s.apiKeyQueueMu.Lock()
	defer s.apiKeyQueueMu.Unlock()
	cached := s.apiKeyQueueCache.Load()
	if cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.value
	}
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	value, err := s.GetAPIKeyQueueSettings(dbCtx)
	if err != nil {
		// Keep the last known policy on transient storage failure. Admission
		// still enforces the existing execution limit even without a policy.
		if cached != nil {
			value = cached.value
		} else {
			value = &APIKeyQueueSettings{}
		}
	}
	s.apiKeyQueueCache.Store(&cachedAPIKeyQueueSettings{value: value, expiresAt: time.Now().Add(5 * time.Second)})
	return value
}
