package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountResponseCodexTicketsUsesConfiguredPolicy(t *testing.T) {
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	h := &AccountHandler{cfg: &config.Config{}}
	require.Empty(t, h.accountResponseFromService(account).CodexTurnTickets)
	require.Empty(t, h.accountListResponseFromService(account).CodexTurnTickets)
	h.cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"configured-model"}, FailClosed: false}
	status := h.accountListResponseFromService(account).CodexTurnTickets
	require.Len(t, status, 1)
	require.Equal(t, "configured-model", status[0].Model)
	require.False(t, status[0].Blocked)
	h.cfg.Gateway.OpenAICodexTicket.FailClosed = true
	require.True(t, h.accountResponseFromService(account).CodexTurnTickets[0].Blocked)
}

func TestAccountResponseCodexTicketsReadsLiveSettingsAfterRestart(t *testing.T) {
	cfg := &config.Config{}
	repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyOpenAICodexTicketEnabled: "true"}}
	settings := service.NewSettingService(repo, cfg)
	h := &AccountHandler{cfg: cfg}
	h.SetCodexTicketSettings(settings)
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeSetupToken}
	require.Len(t, h.accountListResponseFromService(account).CodexTurnTickets, 2)
	require.False(t, cfg.Gateway.OpenAICodexTicket.Enabled)
	repo.values[service.SettingKeyOpenAICodexTicketEnabled] = "false"
	settings.InvalidateOpenAICodexTicketEnabledCache()
	require.Empty(t, h.accountResponseFromService(account).CodexTurnTickets)
}

// 账号列表里的「已暂停」状态必须跟随后台缺票拦截开关，而不是启动时的 yaml 值。
func TestAccountResponseCodexTicketsFollowLiveFailClosedSetting(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, FailClosed: true}
	repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyOpenAICodexTicketFailClosed: "false"}}
	settings := service.NewSettingService(repo, cfg)
	h := &AccountHandler{cfg: cfg}
	h.SetCodexTicketSettings(settings)
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	status := h.accountResponseFromService(account).CodexTurnTickets
	require.Len(t, status, 1)
	require.False(t, status[0].Ready)
	require.False(t, status[0].Blocked, "runtime fail_closed=false must override the yaml fallback")
	repo.values[service.SettingKeyOpenAICodexTicketFailClosed] = "true"
	settings.InvalidateOpenAICodexTicketFailClosedCache()
	require.True(t, h.accountListResponseFromService(account).CodexTurnTickets[0].Blocked)
	require.True(t, cfg.Gateway.OpenAICodexTicket.FailClosed, "shared startup config must stay untouched")
}

// 账号列表里的「就绪/剩余时间」必须按后台目标长度判定，而不是启动时的 yaml 值。
func TestAccountResponseCodexTicketsFollowLiveTargetLength(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, TargetLength: 292}
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	settings := service.NewSettingService(repo, cfg)
	h := &AccountHandler{cfg: cfg}
	h.SetCodexTicketSettings(settings)
	state312 := "gAAAAA" + strings.Repeat("B", 312-6)
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: map[string]any{
		"codex_turn_ticket:gpt-6-astra": map[string]any{
			"state": state312, "length": 312, "model": "gpt-6-astra",
			"captured_at": time.Now().Add(-time.Minute), "expires_at": time.Now().Add(time.Hour),
		},
	}}
	require.False(t, h.accountResponseFromService(account).CodexTurnTickets[0].Ready, "a 312 ticket is not ready while the target is 292")
	repo.values[service.SettingKeyOpenAICodexTicketTargetLength] = "312"
	settings.InvalidateOpenAICodexTicketTargetLengthCache()
	require.True(t, h.accountListResponseFromService(account).CodexTurnTickets[0].Ready)
	require.Equal(t, 292, cfg.Gateway.OpenAICodexTicket.TargetLength, "shared startup config must stay untouched")
}

// 守护摘要只在门票功能开启、账号有门票行时随账号返回。
func TestAccountResponseIncludesCodexTicketWatchdogSummary(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAICodexTicket = config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}}
	h := &AccountHandler{cfg: cfg}
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: map[string]any{
		"codex_turn_ticket:watchdog": map[string]any{"trigger_count": 2, "last_reason": "model_mismatch", "last_model": "gpt-6-astra", "last_response_model": "gpt-5.6-luna"},
	}}
	out := h.accountResponseFromService(account)
	require.NotNil(t, out.CodexTicketWatchdog)
	require.Equal(t, int64(2), out.CodexTicketWatchdog.TriggerCount)
	require.Equal(t, "gpt-5.6-luna", out.CodexTicketWatchdog.LastResponseModel)
	require.NotNil(t, h.accountListResponseFromService(account).CodexTicketWatchdog)
	cfg.Gateway.OpenAICodexTicket.Enabled = false
	require.Nil(t, h.accountResponseFromService(account).CodexTicketWatchdog)
}
