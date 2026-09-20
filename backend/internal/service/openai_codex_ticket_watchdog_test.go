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

func watchdogTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c
}

func watchdogTestService(t *testing.T, repo *codexTicketRefreshRepo) (*OpenAIGatewayService, *Account) {
	t.Helper()
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292, TTLSeconds: 3600}, nil)
	svc.accountRepo = repo
	account := ticketTestAccount(41)
	account.Status = StatusActive
	account.Extra = map[string]any{}
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID: 41, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292,
		CapturedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour),
	})
	repo.updates = nil
	return svc, account
}

// 注入后请求上下文里有回执；没注入（非门控模型 / 无票）时回执为空。
func TestApplyOpenAICodexTicketForRequest_SetsAndClearsReceipt(t *testing.T) {
	svc, account := watchdogTestService(t, &codexTicketRefreshRepo{})
	c := watchdogTestContext(t)
	h := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicketForRequest(context.Background(), c, account, "gpt-6-astra", h))
	require.Equal(t, fakeCodexTicketState(292), h.Get(openAICodexTurnStateHeader))
	receipt := openAICodexTicketReceiptFromContext(c)
	require.NotNil(t, receipt)
	require.Equal(t, int64(41), receipt.accountID)
	require.Equal(t, "gpt-6-astra", receipt.model)

	// failover 到另一个没有票的模型/账号：回执被清掉，不会把旧票误作废。
	require.NoError(t, svc.applyOpenAICodexTicketForRequest(context.Background(), c, account, "gpt-5.6-sol", h))
	require.Nil(t, openAICodexTicketReceiptFromContext(c))
}

// 上游把 astra 按 luna 服务：作废本次注入的票，落库摘要，不动别的模型。
func TestObserveOpenAICodexTicketOutcome_ModelMismatchInvalidatesTicket(t *testing.T) {
	repo := &codexTicketRefreshRepo{}
	svc, account := watchdogTestService(t, repo)
	c := watchdogTestContext(t)
	require.NoError(t, svc.applyOpenAICodexTicketForRequest(context.Background(), c, account, "gpt-6-astra", http.Header{}))

	svc.ObserveOpenAICodexTicketOutcome(c, account, &OpenAIForwardResult{Model: "gpt-6-astra", UpstreamResponseModel: "gpt-5.6-luna"})

	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"), "ticket must be dropped from memory")
	require.Contains(t, repo.updates, openAICodexTicketExtraKey("gpt-6-astra"))
	require.Nil(t, repo.updates[openAICodexTicketExtraKey("gpt-6-astra")], "persisted ticket must be cleared")
	require.Contains(t, repo.updates, openAICodexTicketWatchdogExtraKey)
	status := OpenAICodexTicketWatchdogStatusOf(account)
	require.NotNil(t, status)
	require.Equal(t, int64(1), status.TriggerCount)
	require.Equal(t, openAICodexTicketWatchdogReasonModel, status.LastReason)
	require.Equal(t, "gpt-6-astra", status.LastModel)
	require.Equal(t, "gpt-5.6-luna", status.LastResponseModel)
	require.NotNil(t, status.LastTriggeredAt)
	// 回执只用一次。
	require.Nil(t, openAICodexTicketReceiptFromContext(c))
}

// 响应头带回 312 也算坏票信号。
func TestObserveOpenAICodexTicketOutcome_State312InvalidatesTicket(t *testing.T) {
	repo := &codexTicketRefreshRepo{}
	svc, account := watchdogTestService(t, repo)
	c := watchdogTestContext(t)
	require.NoError(t, svc.applyOpenAICodexTicketForRequest(context.Background(), c, account, "gpt-6-astra", http.Header{}))
	hdr := http.Header{}
	hdr.Set(openAICodexTurnStateHeader, fakeCodexTicketState(312))
	svc.ObserveOpenAICodexTicketOutcome(c, account, &OpenAIForwardResult{Model: "gpt-6-astra", UpstreamResponseModel: "gpt-6-astra", ResponseHeaders: hdr})
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	require.Equal(t, openAICodexTicketWatchdogReasonState312, OpenAICodexTicketWatchdogStatusOf(account).LastReason)
}

// 模型一致、回执 292：什么都不动。
func TestObserveOpenAICodexTicketOutcome_MatchingModelKeepsTicket(t *testing.T) {
	repo := &codexTicketRefreshRepo{}
	svc, account := watchdogTestService(t, repo)
	c := watchdogTestContext(t)
	require.NoError(t, svc.applyOpenAICodexTicketForRequest(context.Background(), c, account, "gpt-6-astra", http.Header{}))
	hdr := http.Header{}
	hdr.Set(openAICodexTurnStateHeader, fakeCodexTicketState(292))
	svc.ObserveOpenAICodexTicketOutcome(c, account, &OpenAIForwardResult{Model: "gpt-6-astra", UpstreamResponseModel: "gpt-6-astra", ResponseHeaders: hdr})
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	require.Empty(t, repo.updates)
	require.Nil(t, OpenAICodexTicketWatchdogStatusOf(account))
}

// 没有回执（本次没注入票）的请求即使模型不符也不作废票：那不是票的锅。
func TestObserveOpenAICodexTicketOutcome_NoReceiptIsIgnored(t *testing.T) {
	repo := &codexTicketRefreshRepo{}
	svc, account := watchdogTestService(t, repo)
	c := watchdogTestContext(t)
	svc.ObserveOpenAICodexTicketOutcome(c, account, &OpenAIForwardResult{Model: "gpt-6-astra", UpstreamResponseModel: "gpt-5.6-luna"})
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	require.Empty(t, repo.updates)
}

// 迟到的响应不能作废之后新采的票。
func TestObserveOpenAICodexTicketOutcome_StaleReceiptDoesNotRevokeNewerTicket(t *testing.T) {
	repo := &codexTicketRefreshRepo{}
	svc, account := watchdogTestService(t, repo)
	c := watchdogTestContext(t)
	require.NoError(t, svc.applyOpenAICodexTicketForRequest(context.Background(), c, account, "gpt-6-astra", http.Header{}))
	// 期间 harvester 采到了一张新票。
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID: 41, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292,
		CapturedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})
	repo.updates = nil
	svc.ObserveOpenAICodexTicketOutcome(c, account, &OpenAIForwardResult{Model: "gpt-6-astra", UpstreamResponseModel: "gpt-5.6-luna"})
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"), "newer ticket must survive a stale receipt")
	require.Empty(t, repo.updates)
}

// 后台关掉守护：只记账不动票。
func TestObserveOpenAICodexTicketOutcome_DisabledByRuntimeSetting(t *testing.T) {
	repo := &codexTicketRefreshRepo{}
	svc, account := watchdogTestService(t, repo)
	settingRepo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{
		SettingKeyOpenAICodexTicketWatchdogEnabled: "false",
	}}}
	svc.settingService = NewSettingService(settingRepo, &config.Config{})
	c := watchdogTestContext(t)
	require.NoError(t, svc.applyOpenAICodexTicketForRequest(context.Background(), c, account, "gpt-6-astra", http.Header{}))
	svc.ObserveOpenAICodexTicketOutcome(c, account, &OpenAIForwardResult{Model: "gpt-6-astra", UpstreamResponseModel: "gpt-5.6-luna"})
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	require.Empty(t, repo.updates)

	settingRepo.values[SettingKeyOpenAICodexTicketWatchdogEnabled] = "true"
	svc.settingService.InvalidateOpenAICodexTicketWatchdogCache()
	require.NoError(t, svc.applyOpenAICodexTicketForRequest(context.Background(), c, account, "gpt-6-astra", http.Header{}))
	svc.ObserveOpenAICodexTicketOutcome(c, account, &OpenAIForwardResult{Model: "gpt-6-astra", UpstreamResponseModel: "gpt-5.6-luna"})
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
}

func TestOpenAICodexTicketWatchdogReason(t *testing.T) {
	reason, _ := openAICodexTicketWatchdogReason(&OpenAIForwardResult{Model: "gpt-6-astra", UpstreamModel: "gpt-6-astra", UpstreamResponseModel: "gpt-5.6-luna"}, "")
	require.Equal(t, openAICodexTicketWatchdogReasonModel, reason)
	// 上游只返回大小写差异不算不符。
	reason, _ = openAICodexTicketWatchdogReason(&OpenAIForwardResult{Model: "gpt-6-astra", UpstreamResponseModel: "GPT-6-Astra"}, "")
	require.Equal(t, "", reason)
	// 结果里没带响应模型时回退到观察器的值。
	reason, _ = openAICodexTicketWatchdogReason(&OpenAIForwardResult{Model: "gpt-6-astra"}, "gpt-5.6-luna")
	require.Equal(t, openAICodexTicketWatchdogReasonModel, reason)
	// 没有任何响应模型、也没有 312：不判定。
	reason, _ = openAICodexTicketWatchdogReason(&OpenAIForwardResult{Model: "gpt-6-astra"}, "")
	require.Equal(t, "", reason)
}

// 守护摘要走 codex_turn_ticket: 前缀，管理员编辑账号时被保留、不被覆盖。
func TestOpenAICodexTicketWatchdogExtraKeyIsServerManaged(t *testing.T) {
	require.True(t, IsOpenAICodexTicketExtraKey(openAICodexTicketWatchdogExtraKey))
	merged := MergeOpenAICodexTicketExtra(map[string]any{openAICodexTicketWatchdogExtraKey: "admin-supplied"}, map[string]any{openAICodexTicketWatchdogExtraKey: map[string]any{"trigger_count": 2}})
	require.Equal(t, map[string]any{"trigger_count": 2}, merged[openAICodexTicketWatchdogExtraKey])
}
