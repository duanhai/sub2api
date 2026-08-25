package securityaudit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	topicSummarySettingEnabled      = "topic_summary_enabled"
	topicSummarySettingBaseURL      = "topic_summary_base_url"
	topicSummarySettingModel        = "topic_summary_model"
	topicSummarySettingAPIKeyCipher = "topic_summary_api_key_ciphertext"
	defaultTopicSummaryModel        = "gpt-5.6-luna"
	maxTopicSummaryBaseURLLength    = 2048
	maxTopicSummaryModelLength      = 200
	maxTopicSummaryAPIKeyLength     = 8192
)

var topicSummarySettingKeys = []string{
	topicSummarySettingEnabled,
	topicSummarySettingBaseURL,
	topicSummarySettingModel,
	topicSummarySettingAPIKeyCipher,
}

type TopicSummarySettings struct {
	Enabled          bool   `json:"enabled"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	APIKeyConfigured bool   `json:"api_key_configured"`
}

type UpdateTopicSummarySettings struct {
	Enabled     bool   `json:"enabled"`
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	APIKey      string `json:"api_key"`
	ClearAPIKey bool   `json:"clear_api_key"`
}

func (s *TopicSummaryService) GetSettings(ctx context.Context) (TopicSummarySettings, error) {
	if s == nil {
		return TopicSummarySettings{}, errors.New("topic summary service is unavailable")
	}
	cfg := s.currentConfig()
	if s.settings != nil && s.encryptor != nil {
		stored, found, err := s.loadStoredConfig(ctx)
		if err != nil {
			return TopicSummarySettings{}, err
		}
		if found {
			cfg = stored
		}
	}
	return publicTopicSummarySettings(cfg), nil
}

func (s *TopicSummaryService) UpdateSettings(ctx context.Context, update UpdateTopicSummarySettings) (TopicSummarySettings, error) {
	if s == nil || s.settings == nil || s.encryptor == nil {
		return TopicSummarySettings{}, errors.New("encrypted topic summary settings are unavailable")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(update.BaseURL), "/")
	model := strings.TrimSpace(update.Model)
	apiKey := strings.TrimSpace(update.APIKey)
	if len(baseURL) > maxTopicSummaryBaseURLLength {
		return TopicSummarySettings{}, errors.New("base URL is too long")
	}
	if len(model) > maxTopicSummaryModelLength {
		return TopicSummarySettings{}, errors.New("model is too long")
	}
	if len(apiKey) > maxTopicSummaryAPIKeyLength {
		return TopicSummarySettings{}, errors.New("API key is too long")
	}
	if model == "" {
		model = defaultTopicSummaryModel
	}
	if baseURL != "" {
		if err := validateTopicSummaryBaseURL(baseURL); err != nil {
			return TopicSummarySettings{}, err
		}
	}

	current := s.currentConfig()
	if stored, found, err := s.loadStoredConfig(ctx); err != nil {
		return TopicSummarySettings{}, err
	} else if found {
		current = stored
	}
	if update.ClearAPIKey {
		apiKey = ""
	} else if apiKey == "" {
		apiKey = current.APIKey
	}
	if update.Enabled && (baseURL == "" || model == "" || apiKey == "") {
		return TopicSummarySettings{}, errors.New("enabled topic summaries require a base URL, model, and API key")
	}

	ciphertext := ""
	if apiKey != "" {
		var err error
		ciphertext, err = s.encryptor.Encrypt(apiKey)
		if err != nil {
			return TopicSummarySettings{}, fmt.Errorf("encrypt topic summary API key: %w", err)
		}
	}
	if err := s.settings.SetMultiple(ctx, map[string]string{
		topicSummarySettingEnabled:      strconv.FormatBool(update.Enabled),
		topicSummarySettingBaseURL:      baseURL,
		topicSummarySettingModel:        model,
		topicSummarySettingAPIKeyCipher: ciphertext,
	}); err != nil {
		return TopicSummarySettings{}, fmt.Errorf("save topic summary settings: %w", err)
	}

	cfg := topicSummaryConfig{
		Enabled: update.Enabled,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}
	s.setConfig(cfg)
	return publicTopicSummarySettings(cfg), nil
}

func (s *TopicSummaryService) loadStoredConfig(ctx context.Context) (topicSummaryConfig, bool, error) {
	if s == nil || s.settings == nil || s.encryptor == nil {
		return topicSummaryConfig{}, false, nil
	}
	values, err := s.settings.GetMultiple(ctx, topicSummarySettingKeys)
	if err != nil {
		return topicSummaryConfig{}, false, fmt.Errorf("load topic summary settings: %w", err)
	}
	rawEnabled, found := values[topicSummarySettingEnabled]
	if !found {
		return topicSummaryConfig{}, false, nil
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(rawEnabled))
	if err != nil {
		return topicSummaryConfig{}, true, errors.New("stored topic summary enabled value is invalid")
	}
	apiKey := ""
	if ciphertext := strings.TrimSpace(values[topicSummarySettingAPIKeyCipher]); ciphertext != "" {
		apiKey, err = s.encryptor.Decrypt(ciphertext)
		if err != nil {
			return topicSummaryConfig{}, true, fmt.Errorf("decrypt topic summary API key: %w", err)
		}
	}
	model := strings.TrimSpace(values[topicSummarySettingModel])
	if model == "" {
		model = defaultTopicSummaryModel
	}
	baseURL := strings.TrimRight(strings.TrimSpace(values[topicSummarySettingBaseURL]), "/")
	return topicSummaryConfig{
		Enabled: enabled,
		APIKey:  strings.TrimSpace(apiKey),
		BaseURL: baseURL,
		Model:   model,
	}, true, nil
}

func publicTopicSummarySettings(cfg topicSummaryConfig) TopicSummarySettings {
	return TopicSummarySettings{
		Enabled:          cfg.Enabled,
		BaseURL:          cfg.BaseURL,
		Model:            cfg.Model,
		APIKeyConfigured: strings.TrimSpace(cfg.APIKey) != "",
	}
}

func validateTopicSummaryBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return errors.New("base URL must be a valid absolute URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("base URL scheme must be http or https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("base URL must not include a query or fragment")
	}
	return nil
}
