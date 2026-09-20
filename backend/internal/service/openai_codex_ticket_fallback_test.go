package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func fallbackTestService(t *testing.T, cfg config.OpenAICodexTicketConfig) *OpenAIGatewayService {
	t.Helper()
	if cfg.FallbackModels == nil {
		cfg.FallbackModels = map[string]string{"gpt-6-astra": "gpt-5.6-sol"}
	}
	return ticketTestService(t, cfg, nil)
}

func TestParseOpenAICodexTicketFallbackModels(t *testing.T) {
	m, err := ParseOpenAICodexTicketFallbackModels(" gpt-6-astra = gpt-5.6-sol \n\ngpt-6-x→gpt-5.6-terra, a=b;")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"gpt-6-astra": "gpt-5.6-sol", "gpt-6-x": "gpt-5.6-terra", "a": "b"}, m)
	m, err = ParseOpenAICodexTicketFallbackModels("")
	require.NoError(t, err)
	require.Empty(t, m)
	for _, bad := range []string{"gpt-6-astra", "gpt-6-astra=", "=gpt-5.6-sol", "gpt-6-astra=gpt-6-astra"} {
		_, err := ParseOpenAICodexTicketFallbackModels(bad)
		require.Error(t, err, bad)
		require.Error(t, ValidateOpenAICodexTicketFallbackModels(bad), bad)
	}
	require.NoError(t, ValidateOpenAICodexTicketFallbackModels(""))
	require.Equal(t, "a=b\ngpt-6-astra=gpt-5.6-sol", FormatOpenAICodexTicketFallbackModels(map[string]string{"gpt-6-astra": "gpt-5.6-sol", "a": "b", "": "x", "y": ""}))
}

// 决策矩阵：只有「门票开、兜底开、拦截关、门控模型、无有效票、映射命中」全满足才改写。
func TestOpenAICodexTicketFallbackModel_DecisionMatrix(t *testing.T) {
	ctx := context.Background()
	account := ticketTestAccount(41)

	svc := fallbackTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292})
	got, ok := svc.openAICodexTicketFallbackModel(ctx, account, "gpt-6-astra")
	require.True(t, ok)
	require.Equal(t, "gpt-5.6-sol", got)

	// 兜底模型自身不再映射；非门控模型不动。
	_, ok = svc.openAICodexTicketFallbackModel(ctx, account, "gpt-5.6-sol")
	require.False(t, ok)
	_, ok = svc.openAICodexTicketFallbackModel(ctx, account, "gpt-5.6-terra")
	require.False(t, ok)

	// 有有效票 → 不兜底（票会被注入）。
	svc.storeOpenAICodexTicket(ctx, account, &openAICodexTicket{AccountID: 41, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	_, ok = svc.openAICodexTicketFallbackModel(ctx, account, "gpt-6-astra")
	require.False(t, ok)
	// 票过期 → 兜底。
	svc.storeOpenAICodexTicket(ctx, account, &openAICodexTicket{AccountID: 41, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, CapturedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)})
	_, ok = svc.openAICodexTicketFallbackModel(ctx, account, "gpt-6-astra")
	require.True(t, ok)

	// 缺票拦截开着 → 不兜底（严格模式按拦截处理）。
	strict := fallbackTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true})
	_, ok = strict.openAICodexTicketFallbackModel(ctx, account, "gpt-6-astra")
	require.False(t, ok)

	// 门票功能关 → 不兜底。
	off := fallbackTestService(t, config.OpenAICodexTicketConfig{Enabled: false})
	_, ok = off.openAICodexTicketFallbackModel(ctx, account, "gpt-6-astra")
	require.False(t, ok)

	// 非 OAuth 账号（apikey）不参与。
	apikey := ticketTestAccount(42)
	apikey.Type = AccountTypeAPIKey
	_, ok = svc.openAICodexTicketFallbackModel(ctx, apikey, "gpt-6-astra")
	require.False(t, ok)
}

// 后台开关与映射表热覆盖 yaml。
func TestOpenAICodexTicketFallback_RuntimeSettingsOverrideYaml(t *testing.T) {
	ctx := context.Background()
	account := ticketTestAccount(41)
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	svc := fallbackTestService(t, config.OpenAICodexTicketConfig{Enabled: true})
	svc.settingService = NewSettingService(repo, &config.Config{})

	got, ok := svc.openAICodexTicketFallbackModel(ctx, account, "gpt-6-astra")
	require.True(t, ok)
	require.Equal(t, "gpt-5.6-sol", got)

	repo.values[SettingKeyOpenAICodexTicketFallbackModels] = "gpt-6-astra=gpt-5.6-terra"
	svc.settingService.InvalidateOpenAICodexTicketFallbackModelsCache()
	got, _ = svc.openAICodexTicketFallbackModel(ctx, account, "gpt-6-astra")
	require.Equal(t, "gpt-5.6-terra", got)

	repo.values[SettingKeyOpenAICodexTicketFallbackEnabled] = "false"
	svc.settingService.InvalidateOpenAICodexTicketFallbackCache()
	_, ok = svc.openAICodexTicketFallbackModel(ctx, account, "gpt-6-astra")
	require.False(t, ok)

	// 保存空映射不报错并回退 yaml；非法映射被拒。
	require.NoError(t, svc.settingService.UpdateSettings(ctx, &SystemSettings{OpenAICodexTicketFallbackEnabled: true, OpenAICodexTicketFallbackModels: ""}))
	require.Error(t, svc.settingService.UpdateSettings(ctx, &SystemSettings{OpenAICodexTicketFallbackEnabled: true, OpenAICodexTicketFallbackModels: "gpt-6-astra"}))
}

// 请求级入口：改写并在 gin 上下文留下 "from→to" 标记。
func TestApplyOpenAICodexTicketFallback_MarksContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := fallbackTestService(t, config.OpenAICodexTicketConfig{Enabled: true})
	model, applied := svc.applyOpenAICodexTicketFallback(context.Background(), c, ticketTestAccount(41), "gpt-6-astra")
	require.True(t, applied)
	require.Equal(t, "gpt-5.6-sol", model)
	require.Equal(t, "gpt-6-astra→gpt-5.6-sol", OpenAICodexTicketFallbackFromContext(c))

	model, applied = svc.applyOpenAICodexTicketFallback(context.Background(), c, ticketTestAccount(41), "gpt-5.6-terra")
	require.False(t, applied)
	require.Equal(t, "gpt-5.6-terra", model)
}
