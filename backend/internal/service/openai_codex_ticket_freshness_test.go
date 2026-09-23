package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestExtractOpenAICodexResponseCookies(t *testing.T) {
	h := http.Header{}
	h.Add("Set-Cookie", "__cf_bm=abc; Path=/; HttpOnly; Secure")
	h.Add("Set-Cookie", "_cfuvid=xyz; Path=/; Domain=.chatgpt.com")
	h.Add("Set-Cookie", "__cf_bm=dup; Path=/")
	require.Equal(t, "__cf_bm=abc; _cfuvid=xyz", extractOpenAICodexResponseCookies(h))
	require.Equal(t, "", extractOpenAICodexResponseCookies(http.Header{}))
	require.Equal(t, "", extractOpenAICodexResponseCookies(nil))

	big := http.Header{}
	for i := 0; i < 200; i++ {
		big.Add("Set-Cookie", "c"+strings.Repeat("x", 3)+string(rune('a'+i%26))+string(rune('a'+i/26))+"="+strings.Repeat("v", 100))
	}
	require.LessOrEqual(t, len(extractOpenAICodexResponseCookies(big)), openAICodexTicketCookieMaxBytes)
}

func TestApplyOpenAICodexTicketCookie(t *testing.T) {
	h := http.Header{}
	applyOpenAICodexTicketCookie(h, &openAICodexTicket{Cookie: "a=1; b=2"})
	require.Equal(t, "a=1; b=2", h.Get("Cookie"))
	h.Set("Cookie", "x=9")
	applyOpenAICodexTicketCookie(h, &openAICodexTicket{Cookie: "a=1"})
	require.Equal(t, "x=9; a=1", h.Get("Cookie"))
	h = http.Header{}
	applyOpenAICodexTicketCookie(h, &openAICodexTicket{})
	require.Empty(t, h.Get("Cookie"))
}

// 打票时抓 Cookie 落到票上；注入票时一并带上；后台关掉则只注入票不带 Cookie。
func TestOpenAICodexTicketCookie_CapturedOnHarvestAndInjected(t *testing.T) {
	hdr := http.Header{}
	hdr.Set(openAICodexTurnStateHeader, fakeCodexTicketState(292))
	hdr.Add("Set-Cookie", "__cf_bm=abc; Path=/; HttpOnly")
	upstream := &codexTicketBackoffUpstream{responses: []*http.Response{{StatusCode: http.StatusOK, Header: hdr, Body: io.NopCloser(strings.NewReader("{}"))}}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600,
		HarvestProxyURL: "socks5h://harvest.example:31", HarvestAttemptTimeoutSeconds: 5,
	}, upstream)
	account := ticketTestAccount(41)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.Equal(t, "__cf_bm=abc", ticket.Cookie)

	out := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", out))
	require.Equal(t, fakeCodexTicketState(292), out.Get(openAICodexTurnStateHeader))
	require.Equal(t, "__cf_bm=abc", out.Get("Cookie"))

	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAICodexTicketCookieEnabled: "false"}}}
	svc.settingService = NewSettingService(repo, &config.Config{})
	out = http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", out))
	require.Equal(t, fakeCodexTicketState(292), out.Get(openAICodexTurnStateHeader))
	require.Empty(t, out.Get("Cookie"))
}

// TTL 后台热改只影响之后打到的票。
func TestOpenAICodexTicketTTL_RuntimeSetting(t *testing.T) {
	mk := func() *http.Response {
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(292))
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("{}"))}
	}
	upstream := &codexTicketBackoffUpstream{responses: []*http.Response{mk()}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600,
		HarvestProxyURL: "socks5h://harvest.example:31", HarvestAttemptTimeoutSeconds: 5,
	}, upstream)
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{SettingKeyOpenAICodexTicketTTLSeconds: "300"}}}
	svc.settingService = NewSettingService(repo, &config.Config{})
	require.Equal(t, 300, svc.openAICodexTicketConfig().TTLSeconds)
	account := ticketTestAccount(41)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.WithinDuration(t, ticket.CapturedAt.Add(300*time.Second), ticket.ExpiresAt, time.Second)

	for _, bad := range []string{"10", "999999", "abc"} {
		repo.values[SettingKeyOpenAICodexTicketTTLSeconds] = bad
		svc.settingService.InvalidateOpenAICodexTicketTTLCache()
		require.Equal(t, 3600, svc.openAICodexTicketConfig().TTLSeconds, bad)
	}
}

func TestOpenAICodexTicketReharvestDue(t *testing.T) {
	cap := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	tk := &openAICodexTicket{AccountID: 10, Model: "gpt-6-astra", CapturedAt: cap}
	after := 45 * time.Second
	require.False(t, tk.reharvestDue(cap.Add(44*time.Second), after), "never before N")
	require.True(t, tk.reharvestDue(cap.Add(90*time.Second), after), "always by 2N")
	require.False(t, tk.reharvestDue(cap.Add(time.Hour), 0), "disabled when 0")
	// 同一张票算出的时刻稳定。
	var first time.Time
	for s := 45; s <= 90; s++ {
		if tk.reharvestDue(cap.Add(time.Duration(s)*time.Second), after) {
			first = cap.Add(time.Duration(s) * time.Second)
			break
		}
	}
	require.False(t, first.IsZero())
	require.True(t, tk.reharvestDue(first, after))
	require.True(t, tk.reharvestDue(first.Add(time.Second), after))
}

// 开持续打票：有效票未临近过期也会继续打；打到 312 保留旧票，打到 292 换新票。
func TestRefreshOpenAICodexTickets_ReharvestKeepsOldUntilNewArrives(t *testing.T) {
	mk := func(n int) *http.Response {
		h := http.Header{}
		h.Set(openAICodexTurnStateHeader, fakeCodexTicketState(n))
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("{}"))}
	}
	upstream := &codexTicketBackoffUpstream{responses: []*http.Response{mk(312), mk(292)}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{
		Enabled: true, TargetLength: 292, TTLSeconds: 3600, RefreshBeforeSeconds: 600,
		HarvestProxyURL: "socks5h://harvest.example:31", HarvestAttemptTimeoutSeconds: 5,
		Models: []string{"gpt-6-astra"}, ReharvestAfterSeconds: 30,
	}, upstream)
	account := ticketTestAccount(41)
	account.Status = StatusActive
	svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}
	old := &openAICodexTicket{AccountID: 41, Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292,
		CapturedAt: time.Now().Add(-5 * time.Minute), ExpiresAt: time.Now().Add(55 * time.Minute)}
	svc.storeOpenAICodexTicket(context.Background(), account, old)

	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 1, upstream.count(), "reharvest due → probed despite a valid ticket")
	require.True(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra").CapturedAt.Equal(old.CapturedAt), "312 miss keeps the old ticket")

	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 2, upstream.count())
	fresh := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.True(t, fresh.CapturedAt.After(old.CapturedAt), "292 hit replaces with the newer ticket")

	// 新票刚到手：未到持续打票时刻 → 不打。
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 2, upstream.count())

	// 关闭持续打票：原来的「有效且未临近过期就不打」行为不变。
	off := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, TargetLength: 292, TTLSeconds: 3600, RefreshBeforeSeconds: 600,
		HarvestProxyURL: "socks5h://harvest.example:31", HarvestAttemptTimeoutSeconds: 5, Models: []string{"gpt-6-astra"}}, &codexTicketBackoffUpstream{})
	off.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}
	off.storeOpenAICodexTicket(context.Background(), account, old)
	off.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 0, off.httpUpstream.(*codexTicketBackoffUpstream).count())
}

func TestValidateOpenAICodexTicketFreshnessSettings(t *testing.T) {
	require.NoError(t, ValidateOpenAICodexTicketTTLSeconds(60))
	require.NoError(t, ValidateOpenAICodexTicketTTLSeconds(86400))
	require.Error(t, ValidateOpenAICodexTicketTTLSeconds(59))
	require.NoError(t, ValidateOpenAICodexTicketReharvestAfterSeconds(0))
	require.NoError(t, ValidateOpenAICodexTicketReharvestAfterSeconds(10))
	require.Error(t, ValidateOpenAICodexTicketReharvestAfterSeconds(5))
	require.Error(t, ValidateOpenAICodexTicketReharvestAfterSeconds(3601))
	require.NoError(t, ValidateUpstreamModelNotFoundCooldownSeconds(0))
	require.Error(t, ValidateUpstreamModelNotFoundCooldownSeconds(-1))
}
