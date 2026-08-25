package securityaudit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type topicSummarySettingRepoStub struct {
	values map[string]string
}

func (r *topicSummarySettingRepoStub) Get(_ context.Context, key string) (*service.Setting, error) {
	value, ok := r.values[key]
	if !ok {
		return nil, service.ErrSettingNotFound
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (r *topicSummarySettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *topicSummarySettingRepoStub) Set(_ context.Context, key, value string) error {
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

func (r *topicSummarySettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string)
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *topicSummarySettingRepoStub) SetMultiple(ctx context.Context, values map[string]string) error {
	for key, value := range values {
		if err := r.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *topicSummarySettingRepoStub) GetAll(_ context.Context) (map[string]string, error) {
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *topicSummarySettingRepoStub) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

type topicSummaryEncryptorStub struct{}

func (topicSummaryEncryptorStub) Encrypt(value string) (string, error) {
	return "encrypted:" + value, nil
}

func (topicSummaryEncryptorStub) Decrypt(value string) (string, error) {
	plain, ok := strings.CutPrefix(value, "encrypted:")
	if !ok {
		return "", errors.New("invalid ciphertext")
	}
	return plain, nil
}

func TestTopicSummarySettingsMigrateEnvironmentAndApplyWithoutRestart(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "legacy-key")
	t.Setenv("OPENAI_BASE_URL", "https://legacy.example/v1")
	t.Setenv("TOPIC_SUMMARY_MODEL", defaultTopicSummaryModel)
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	repo := &topicSummarySettingRepoStub{values: make(map[string]string)}
	summaryService := NewTopicSummaryService(redisClient, repo, topicSummaryEncryptorStub{})

	legacy, err := summaryService.GetSettings(t.Context())
	require.NoError(t, err)
	require.True(t, legacy.Enabled)
	require.True(t, legacy.APIKeyConfigured)
	require.Equal(t, "https://legacy.example/v1", legacy.BaseURL)
	require.Equal(t, "true", repo.values[topicSummarySettingEnabled])
	require.Equal(t, "encrypted:legacy-key", repo.values[topicSummarySettingAPIKeyCipher])

	updated, err := summaryService.UpdateSettings(t.Context(), UpdateTopicSummarySettings{
		Enabled: true,
		BaseURL: "https://panel.example",
		Model:   "gpt-5.6-luna",
	})
	require.NoError(t, err)
	require.True(t, updated.Enabled)
	require.True(t, updated.APIKeyConfigured)
	require.Equal(t, "encrypted:legacy-key", repo.values[topicSummarySettingAPIKeyCipher])
	require.NotEqual(t, "legacy-key", repo.values[topicSummarySettingAPIKeyCipher])
	require.Equal(t, "legacy-key", summaryService.currentConfig().APIKey)
	require.Equal(t, "https://panel.example/v1/responses", summaryService.currentConfig().ResponsesURL)

	_, err = summaryService.UpdateSettings(t.Context(), UpdateTopicSummarySettings{
		Enabled:     false,
		BaseURL:     "https://panel.example",
		Model:       defaultTopicSummaryModel,
		ClearAPIKey: true,
	})
	require.NoError(t, err)
	require.False(t, summaryService.Enabled())
	require.Empty(t, repo.values[topicSummarySettingAPIKeyCipher])

	summaryService.Observe(Request{
		RequestID: "disabled", APIKeyID: 1, Protocol: "openai_responses",
		Body: []byte(`{"input":"must not be distilled"}`),
	})
	time.Sleep(20 * time.Millisecond)
	require.Empty(t, summaryService.sessions)
}

func TestTopicSummarySettingsRejectEnabledWithoutKey(t *testing.T) {
	service := NewTopicSummaryService(nil, &topicSummarySettingRepoStub{values: map[string]string{}}, topicSummaryEncryptorStub{})
	_, err := service.UpdateSettings(t.Context(), UpdateTopicSummarySettings{
		Enabled: true,
		BaseURL: "https://example.com",
		Model:   defaultTopicSummaryModel,
	})
	require.EqualError(t, err, "enabled topic summaries require a base URL, model, and API key")
}
