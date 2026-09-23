package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
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
		r.values[k] = v
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

// 探测周期是后台热设置：yaml 只是回退值。
func TestCodexTicketProbeIntervalRuntimeSettingOverridesYaml(t *testing.T) {
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	settings := NewSettingService(repo, &config.Config{})
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, HarvestProbeIntervalSeconds: 6}, nil)
	svc.settingService = settings
	require.Equal(t, 6, svc.openAICodexTicketConfig().HarvestProbeIntervalSeconds)
	repo.values[SettingKeyOpenAICodexTicketHarvestProbeIntervalSeconds] = "30"
	settings.InvalidateOpenAICodexTicketHarvestProbeIntervalCache()
	require.Equal(t, 30, svc.openAICodexTicketConfig().HarvestProbeIntervalSeconds)
	for _, bad := range []string{"1", "601", "abc", "-5"} {
		repo.values[SettingKeyOpenAICodexTicketHarvestProbeIntervalSeconds] = bad
		settings.InvalidateOpenAICodexTicketHarvestProbeIntervalCache()
		require.Equal(t, 6, svc.openAICodexTicketConfig().HarvestProbeIntervalSeconds, bad)
	}
	require.NoError(t, svc.settingService.UpdateSettings(context.Background(), &SystemSettings{}))
	require.Equal(t, "", repo.values[SettingKeyOpenAICodexTicketHarvestProbeIntervalSeconds])
	require.Error(t, svc.settingService.UpdateSettings(context.Background(), &SystemSettings{OpenAICodexTicketHarvestProbeIntervalSeconds: 2}))
}

// codexTicketBackoffUpstream 是并发安全的探测桩：按顺序吐出预设响应，记录请求数。
type codexTicketBackoffUpstream struct {
	mu        sync.Mutex
	responses []*http.Response
	requests  int
}

func (u *codexTicketBackoffUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if req != nil && req.Body != nil {
		_ = req.Body.Close()
	}
	u.requests++
	if len(u.responses) == 0 {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	resp := u.responses[0]
	u.responses = u.responses[1:]
	return resp, nil
}

func (u *codexTicketBackoffUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *codexTicketBackoffUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests
}

// 上游 429 按账号指数退避：退避窗口内整个账号不再探测，任何非 429 结果清零。
func TestHarvestOpenAICodexTicket_BacksOffOn429(t *testing.T) {
	mk := func(status int, state string) *http.Response {
		h := http.Header{}
		if state != "" {
			h.Set(openAICodexTurnStateHeader, state)
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader("{}"))}
	}
	upstream := &codexTicketBackoffUpstream{responses: []*http.Response{
		mk(http.StatusTooManyRequests, ""),           // 第一轮 astra
		mk(http.StatusTooManyRequests, ""),           // 第一轮 sol
		mk(http.StatusTooManyRequests, ""),           // 第三次 429
		mk(http.StatusOK, fakeCodexTicketState(312)), // 312 miss → 清零
		mk(http.StatusOK, fakeCodexTicketState(292)),
		mk(http.StatusOK, fakeCodexTicketState(292)),
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled:                      true,
		TargetLength:                 292,
		TTLSeconds:                   3600,
		HarvestProxyURL:              "socks5h://harvest.example:31",
		HarvestAttemptTimeoutSeconds: 5,
		HarvestProbeIntervalSeconds:  30,
	}, upstream)
	account := ticketTestAccount(41)
	account.Status = StatusActive
	svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}

	// 第一轮：两模型各打一发，都 429。退避以 30s 为基数指数增长：30s → 60s。
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 2, upstream.count())
	b := svc.openAICodexTicketBackoffFor(41)
	require.Equal(t, 2, b.consecutive429)
	require.WithinDuration(t, time.Now().Add(60*time.Second), b.until, 3*time.Second)

	// 退避窗口内：整轮跳过，不发任何请求。
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 2, upstream.count())

	// 窗口过去后再打，第三次 429 → 120s。
	svc.openaiCodexTicketBackoff.Store(int64(41), openAICodexTicketBackoff{consecutive429: 2, until: time.Now().Add(-time.Second)})
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	b = svc.openAICodexTicketBackoffFor(41)
	require.Equal(t, 3, b.consecutive429)
	require.WithinDuration(t, time.Now().Add(120*time.Second), b.until, 3*time.Second)

	// 非 429 结果（这里是 312 miss）清零退避。
	svc.openaiCodexTicketBackoff.Store(int64(41), openAICodexTicketBackoff{consecutive429: 3, until: time.Now().Add(-time.Second)})
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	require.Equal(t, 0, svc.openAICodexTicketBackoffFor(41).consecutive429)
	require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))

	// 之后正常命中。
	svc.refreshOpenAICodexTickets(context.Background())
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-5.6-sol"))
}

func TestOpenAICodexTicketBackoffCapsAtFiveMinutes(t *testing.T) {
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true}, nil)
	now := time.Now()
	var wait time.Duration
	for i := 0; i < 12; i++ {
		wait = svc.noteOpenAICodexTicketProbe429(7, now, 30*time.Second)
	}
	require.Equal(t, openAICodexTicketBackoffMax, wait)
	require.Equal(t, 12, svc.openAICodexTicketBackoffFor(7).consecutive429)
	svc.clearOpenAICodexTicketBackoff(7)
	require.Equal(t, 0, svc.openAICodexTicketBackoffFor(7).consecutive429)
}
