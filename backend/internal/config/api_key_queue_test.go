package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestLoadAPIKeyQueuesAndValidation(t *testing.T) {
	resetViperWithJWTSecret(t)
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader("gateway:\n  api_key_queues:\n    '123':\n      max_waiting: 3\n      timeout_seconds: 20\n")))
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, APIKeyQueueConfig{MaxWaiting: 3, TimeoutSeconds: 20}, cfg.Gateway.APIKeyQueues["123"])
	for _, tc := range []struct {
		id               string
		waiting, seconds int
	}{
		{"sk-secret", 3, 20}, {"0", 3, 20}, {"01", 3, 20}, {"1", 0, 20}, {"1", 101, 20}, {"1", 3, 0}, {"1", 3, 61},
	} {
		cfg.Gateway.APIKeyQueues = map[string]APIKeyQueueConfig{tc.id: {MaxWaiting: tc.waiting, TimeoutSeconds: tc.seconds}}
		require.ErrorContains(t, cfg.Validate(), "api_key_queues")
	}
}
