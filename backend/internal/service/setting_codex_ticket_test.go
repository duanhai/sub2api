package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexTicketSettingRepo struct {
	*codexPolicyMigrationRepoStub
	err error
}

func (r *codexTicketSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.codexPolicyMigrationRepoStub.GetValue(ctx, key)
}

// SetMultiple 让 UpdateSettings 可以在本 stub 上落库（父 stub 对该方法 panic）。
func (r *codexTicketSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	if r.err != nil {
		return r.err
	}
	for k, v := range settings {
		r.codexPolicyMigrationRepoStub.values[k] = v
	}
	return nil
}

func TestCodexTicketEnabledRuntimeSettingOverridesYaml(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(repo, &config.Config{})
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: false, FailClosed: true}, nil)
	svc.settingService = settings
	account := ticketTestAccount(41)
	svc.storeOpenAICodexTicket(context.Background(), account, &openAICodexTicket{
		AccountID:  41,
		Model:      "gpt-6-astra",
		State:      fakeCodexTicketState(292),
		Length:     292,
		CapturedAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.False(t, svc.openAICodexTicketEnabled())
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))

	repo.values[SettingKeyOpenAICodexTicketEnabled] = "true"
	settings.InvalidateOpenAICodexTicketEnabledCache()
	require.True(t, svc.openAICodexTicketEnabled())
	h = http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, fakeCodexTicketState(292), h.Get(openAICodexTurnStateHeader))

	repo.values[SettingKeyOpenAICodexTicketEnabled] = "false"
	settings.InvalidateOpenAICodexTicketEnabledCache()
	require.False(t, svc.openAICodexTicketEnabled())
	h = http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))
}

func TestRefreshOpenAICodexTickets_DisabledSkipsHarvest(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:         false,
		HarvestProxyURL: "socks5h://proxy.example.com:1080",
	}, upstream)
	svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*ticketTestAccount(41)}}
	svc.refreshOpenAICodexTickets(context.Background())
	require.Empty(t, upstream.requests)
}

func TestCodexTicketProxyRuntimeSettingAndFallback(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(repo, &config.Config{})
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{HarvestProxyURL: "http://fallback.example.com:8080"}, nil)
	svc.settingService = settings
	require.Equal(t, "http://fallback.example.com:8080", svc.openAICodexTicketHarvestProxyURL())
	repo.values[SettingKeyOpenAICodexTicketHarvestProxyURL] = "socks5h://user:secret@first.example.com:1080"
	settings.InvalidateOpenAICodexTicketHarvestProxyCache()
	require.Equal(t, repo.values[SettingKeyOpenAICodexTicketHarvestProxyURL], svc.openAICodexTicketHarvestProxyURL())
	repo.values[SettingKeyOpenAICodexTicketHarvestProxyURL] = "http://second.example.com:8080"
	settings.InvalidateOpenAICodexTicketHarvestProxyCache()
	require.Equal(t, "http://second.example.com:8080", svc.openAICodexTicketHarvestProxyURL())
	// Simulate another instance's settings write after the local cache expires.
	repo.values[SettingKeyOpenAICodexTicketHarvestProxyURL] = "https://third.example.com:443"
	settings.openAICodexTicketHarvestProxyCache.Store(&cachedOpenAICodexTicketHarvestProxy{value: "http://second.example.com:8080", expiresAt: time.Now().Add(-time.Second).UnixNano()})
	require.Equal(t, "https://third.example.com:443", svc.openAICodexTicketHarvestProxyURL())
	repo.err = errors.New("database unavailable")
	settings.openAICodexTicketHarvestProxyCache.Store(&cachedOpenAICodexTicketHarvestProxy{value: "https://third.example.com:443", expiresAt: 0})
	require.Equal(t, "https://third.example.com:443", svc.openAICodexTicketHarvestProxyURL())
}

func TestCodexTicketProxyMaskAndValidation(t *testing.T) {
	for _, raw := range []string{"http://user:secret@proxy.example.com:8080", "socks5h://user:secret@proxy.example.com:1080", "https://user:secret@[::1]:443"} {
		require.NoError(t, ValidateOpenAICodexTicketHarvestProxyURL(raw))
		masked := MaskProxyURL(raw)
		require.NotContains(t, masked, "secret")
		require.True(t, IsMaskedProxyURL(masked))
	}
	require.True(t, IsMaskedProxyURL(""))
	require.False(t, IsMaskedProxyURL("http://user:secret***suffix@proxy.example.com:8080"))
	for _, raw := range []string{"user:secret@host:1234", "http://user:secret@", "ftp://user:secret@host:1234", "http://user:secret@host:99999", "http://host:1234/?password=secret", "http://host:1234/#secret", "http://user:secret%zz@host:1234"} {
		err := ValidateOpenAICodexTicketHarvestProxyURL(raw)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
		require.Empty(t, MaskProxyURL(raw))
	}
}

func TestCodexTicketSettingsRefreshDoesNotMutateSharedConfig(t *testing.T) {
	cfg := &config.Config{}
	svc := NewSettingService(&codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAICodexTicketEnabled: "true"}}}, cfg)
	svc.refreshCachedSettings(&SystemSettings{OpenAICodexTicketEnabled: true})
	require.False(t, cfg.Gateway.OpenAICodexTicket.Enabled, "runtime settings must not write the shared immutable startup configuration")
	require.True(t, svc.GetOpenAICodexTicketEnabled(context.Background(), false))
}

// 缺票拦截是后台热开关：yaml 只是回退值，后台一改立即生效、无需重启。
func TestCodexTicketFailClosedRuntimeSettingOverridesYaml(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(repo, &config.Config{})
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.settingService = settings
	account := ticketTestAccount(41) // 无票

	// 后台未设置 → 跟随 yaml（严格模式）：无票账号暂停调度，出站直接失败。
	require.True(t, svc.openAICodexTicketFailClosed())
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.ErrorIs(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h), ErrOpenAICodexTicketUnavailable)

	// 后台关闭缺票拦截 → 不拦号、不碰请求头，客户端自带的 turn-state 原样放行。
	repo.values[SettingKeyOpenAICodexTicketFailClosed] = "false"
	settings.InvalidateOpenAICodexTicketFailClosedCache()
	require.False(t, svc.openAICodexTicketFailClosed())
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	h = http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))

	// 再次开启 → 恢复拦截。
	repo.values[SettingKeyOpenAICodexTicketFailClosed] = "true"
	settings.InvalidateOpenAICodexTicketFailClosedCache()
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	h = http.Header{}
	require.ErrorIs(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h), ErrOpenAICodexTicketUnavailable)
}

// 默认（yaml 未写 fail_closed、后台未设置）就是放行：门票是增强能力，不是业务前置条件。
func TestCodexTicketFailClosedDefaultsToFailOpen(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	account := ticketTestAccount(41)
	require.False(t, svc.openAICodexTicketFailClosed())
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))
}

func TestCodexTicketFailClosedSettingDoesNotMutateSharedConfig(t *testing.T) {
	cfg := &config.Config{}
	svc := NewSettingService(&codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAICodexTicketFailClosed: "true"}}}, cfg)
	svc.refreshCachedSettings(&SystemSettings{OpenAICodexTicketFailClosed: true})
	require.False(t, cfg.Gateway.OpenAICodexTicket.FailClosed, "runtime settings must not write the shared immutable startup configuration")
	require.True(t, svc.GetOpenAICodexTicketFailClosed(context.Background(), false))
}

// 目标长度是后台热设置：上游铸票格式漂移（生产曾长期只见 312）时无需改 yaml、无需重启。
func TestCodexTicketTargetLengthRuntimeSettingOverridesYaml(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(repo, &config.Config{})
	state312 := fakeCodexTicketState(312)
	mk := func() *http.Response {
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, state312)
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{mk(), mk()}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 292,
		TTLSeconds:                   3600,
		HarvestProxyURL:              "socks5h://harvest.example:31",
		HarvestAttemptTimeoutSeconds: 5,
	}, upstream)
	svc.settingService = settings
	account := ticketTestAccount(41)

	// 后台未设置 → 跟随 yaml 292：上游给的 312 是 miss，不落库。
	require.Equal(t, 292, svc.openAICodexTicketConfig().TargetLength)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))

	// 后台改成 312 → 同样的上游响应立即被收下并注入。
	repo.values[SettingKeyOpenAICodexTicketTargetLength] = "312"
	settings.InvalidateOpenAICodexTicketTargetLengthCache()
	require.Equal(t, 312, svc.openAICodexTicketConfig().TargetLength)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.Equal(t, 312, ticket.Length)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, state312, h.Get(openAICodexTurnStateHeader))

	// 改回 292 → 已存的 312 票立即失效；默认 fail-open，保留客户端自带状态。
	repo.values[SettingKeyOpenAICodexTicketTargetLength] = "292"
	settings.InvalidateOpenAICodexTicketTargetLengthCache()
	h = http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))

	// 越界或非数字回退 yaml。
	for _, bad := range []string{"10", "5000", "abc", "-1"} {
		repo.values[SettingKeyOpenAICodexTicketTargetLength] = bad
		settings.InvalidateOpenAICodexTicketTargetLengthCache()
		require.Equal(t, 292, svc.openAICodexTicketConfig().TargetLength, bad)
	}
}

func TestValidateOpenAICodexTicketTargetLength(t *testing.T) {
	for _, ok := range []int{64, 292, 312, 4096} {
		require.NoError(t, ValidateOpenAICodexTicketTargetLength(ok))
	}
	for _, bad := range []int{0, -1, 63, 4097} {
		require.Error(t, ValidateOpenAICodexTicketTargetLength(bad))
	}
}

// 程序化/部分更新不带目标长度（0）时必须能保存，且回退 yaml 默认，不能被区间校验拒绝。
func TestUpdateSettingsWithoutCodexTicketTargetLengthKeepsDefault(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{
		SettingKeyOpenAICodexTicketTargetLength: "312",
	}}}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.UpdateSettings(context.Background(), &SystemSettings{}))
	require.Equal(t, "", repo.values[SettingKeyOpenAICodexTicketTargetLength])
	require.Equal(t, 292, svc.GetOpenAICodexTicketTargetLength(context.Background(), 292))

	require.Error(t, svc.UpdateSettings(context.Background(), &SystemSettings{OpenAICodexTicketTargetLength: 10}))
	require.Equal(t, "", repo.values[SettingKeyOpenAICodexTicketTargetLength], "rejected value must not be written")
}
