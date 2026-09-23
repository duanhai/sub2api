package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingsCodexTicketProxyWriteReadAndHotReload(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketHarvestProxyURL
	oldProxy := "http://user:old-secret@old.example.com:8080"
	newProxy := "socks5h://user:new-secret@new.example.com:1080"
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{key: oldProxy})
	require.Equal(t, oldProxy, h.settingService.GetOpenAICodexTicketHarvestProxyURL(context.Background()))
	rec := doUpdateSettings(t, h, map[string]any{key: newProxy}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, newProxy, repo.values[key])
	require.Equal(t, newProxy, h.settingService.GetOpenAICodexTicketHarvestProxyURL(context.Background()))
	require.NotContains(t, rec.Body.String(), "new-secret")
	require.Contains(t, rec.Body.String(), `"openai_codex_ticket_harvest_proxy_configured":true`)
	// Omission, empty input and the masked GET value all preserve the real secret.
	for _, body := range []map[string]any{{"site_name": "updated"}, {key: ""}, {key: service.MaskProxyURL(newProxy)}} {
		rec = doUpdateSettings(t, h, body, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, newProxy, repo.values[key])
	}
	get := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(get)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, get.Code)
	require.NotContains(t, get.Body.String(), "new-secret")
	require.Contains(t, get.Body.String(), "new.example.com")
}

func TestSettingsCodexTicketRejectInvalidProxyWithoutLeakingPassword(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketHarvestProxyURL
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{key: "http://previous.example.com:8080"})
	rec := doUpdateSettings(t, h, map[string]any{key: "ftp://user:invalid-secret@proxy.example.com:21"}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "invalid-secret")
	require.Equal(t, "http://previous.example.com:8080", repo.values[key])
}

func TestSettingsCodexTicketFailClosedWriteReadAndHotReload(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketFailClosed
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	// 未设置时跟随 yaml 回退值（默认 false）。
	require.False(t, h.settingService.GetOpenAICodexTicketFailClosed(context.Background(), false))

	rec := doUpdateSettings(t, h, map[string]any{key: true}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "true", repo.values[key])
	require.True(t, h.settingService.GetOpenAICodexTicketFailClosed(context.Background(), false), "must take effect without restart")
	require.Contains(t, rec.Body.String(), `"openai_codex_ticket_fail_closed":true`)

	// 省略该字段的保存不得把开关改回去。
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "updated"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "true", repo.values[key])

	rec = doUpdateSettings(t, h, map[string]any{key: false}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[key])
	require.False(t, h.settingService.GetOpenAICodexTicketFailClosed(context.Background(), true))

	get := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(get)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, get.Code)
	require.Contains(t, get.Body.String(), `"openai_codex_ticket_fail_closed":false`)
}

func TestSettingsCodexTicketTargetLengthWriteReadValidate(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketTargetLength
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	require.Equal(t, 292, h.settingService.GetOpenAICodexTicketTargetLength(context.Background(), 292))

	rec := doUpdateSettings(t, h, map[string]any{key: 312}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "312", repo.values[key])
	require.Equal(t, 312, h.settingService.GetOpenAICodexTicketTargetLength(context.Background(), 292), "must take effect without restart")
	require.Contains(t, rec.Body.String(), `"openai_codex_ticket_target_length":312`)

	// 越界值被拒绝，且不覆盖已保存的值。
	rec = doUpdateSettings(t, h, map[string]any{key: 10}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "312", repo.values[key])

	// 省略字段保持原值。
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "updated"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "312", repo.values[key])

	get := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(get)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, get.Code)
	require.Contains(t, get.Body.String(), `"openai_codex_ticket_target_length":312`)
}

func TestSettingsCodexTicketProbeIntervalWriteReadValidate(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketHarvestProbeIntervalSeconds
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	require.Equal(t, 6, h.settingService.GetOpenAICodexTicketHarvestProbeIntervalSeconds(context.Background(), 6))
	rec := doUpdateSettings(t, h, map[string]any{key: 30}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "30", repo.values[key])
	require.Equal(t, 30, h.settingService.GetOpenAICodexTicketHarvestProbeIntervalSeconds(context.Background(), 6))
	require.Contains(t, rec.Body.String(), `"openai_codex_ticket_harvest_probe_interval_seconds":30`)
	rec = doUpdateSettings(t, h, map[string]any{key: 2}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "30", repo.values[key])
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "updated"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "30", repo.values[key])
}

func TestSettingsCodexTicketWatchdogDefaultsOnAndHotReloads(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketWatchdogEnabled
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	require.True(t, h.settingService.GetOpenAICodexTicketWatchdogEnabled(context.Background(), true))
	get := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(get)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Contains(t, get.Body.String(), `"openai_codex_ticket_watchdog_enabled":true`)

	rec := doUpdateSettings(t, h, map[string]any{key: false}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[key])
	require.False(t, h.settingService.GetOpenAICodexTicketWatchdogEnabled(context.Background(), true))
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "updated"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[key])
}

func TestSettingsCodexTicketFallbackWriteReadValidate(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	rec := doUpdateSettings(t, h, map[string]any{"openai_codex_ticket_fallback_enabled": false, "openai_codex_ticket_fallback_models": "gpt-6-astra=gpt-5.6-terra"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[service.SettingKeyOpenAICodexTicketFallbackEnabled])
	require.Equal(t, "gpt-6-astra=gpt-5.6-terra", repo.values[service.SettingKeyOpenAICodexTicketFallbackModels])
	require.Contains(t, rec.Body.String(), `"openai_codex_ticket_fallback_models":"gpt-6-astra=gpt-5.6-terra"`)
	rec = doUpdateSettings(t, h, map[string]any{"openai_codex_ticket_fallback_models": "not-a-mapping"}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "gpt-6-astra=gpt-5.6-terra", repo.values[service.SettingKeyOpenAICodexTicketFallbackModels])
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "updated"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[service.SettingKeyOpenAICodexTicketFallbackEnabled])
}
