package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAICodexTicketExtraKeyPrefix  = "codex_turn_ticket:"
	openAICodexAstraMinVersion       = "0.153.4"
	openAICodexTicketStatePrefix     = "gAAAAA"
	openAICodexTicketDefaultModel    = "gpt-6-astra"
	openAICodexTicketDefaultSolModel = "gpt-5.6-sol"
	// openAICodexTicketDefaultTargetLength 是历史上被认定为「好票」的 turn-state 长度。
	// 上游铸票格式可能变化（生产曾长期只返回 312），因此该值可在后台热改。
	openAICodexTicketDefaultTargetLength = 292
	openAICodexTicketMinTargetLength     = 64
	openAICodexTicketMaxTargetLength     = 4096
	// 探测周期：默认 6 秒是原设计；生产上叠加业务流量会招来上游 429，因此可热改。
	openAICodexTicketDefaultProbeIntervalSeconds = 6
	openAICodexTicketMinProbeIntervalSeconds     = 5
	openAICodexTicketMaxProbeIntervalSeconds     = 600
	// 429 退避：按账号累计连续 429 次数，退避 = 探测周期 × 2^(n-1)，上限 5 分钟。
	openAICodexTicketBackoffMax = 5 * time.Minute
)

// ValidateOpenAICodexTicketHarvestProbeInterval 校验后台提交的探测周期（秒）。
func ValidateOpenAICodexTicketHarvestProbeInterval(n int) error {
	if n < openAICodexTicketMinProbeIntervalSeconds || n > openAICodexTicketMaxProbeIntervalSeconds {
		return fmt.Errorf("codex ticket probe interval must be between %d and %d seconds", openAICodexTicketMinProbeIntervalSeconds, openAICodexTicketMaxProbeIntervalSeconds)
	}
	return nil
}

// openAICodexTicketBackoff 是单个账号的 429 退避状态。上游限流按账号计，
// 不按模型，所以两模型共用一份。
type openAICodexTicketBackoff struct {
	consecutive429 int
	until          time.Time
}

func (s *OpenAIGatewayService) openAICodexTicketBackoffFor(accountID int64) openAICodexTicketBackoff {
	if raw, ok := s.openaiCodexTicketBackoff.Load(accountID); ok {
		if b, ok := raw.(openAICodexTicketBackoff); ok {
			return b
		}
	}
	return openAICodexTicketBackoff{}
}

// noteOpenAICodexTicketProbe429 记录一次 429 并延长退避窗口，返回本次退避时长。
func (s *OpenAIGatewayService) noteOpenAICodexTicketProbe429(accountID int64, now time.Time, base time.Duration) time.Duration {
	b := s.openAICodexTicketBackoffFor(accountID)
	b.consecutive429++
	if base <= 0 {
		base = time.Duration(openAICodexTicketDefaultProbeIntervalSeconds) * time.Second
	}
	wait := base
	for i := 1; i < b.consecutive429 && wait < openAICodexTicketBackoffMax; i++ {
		wait *= 2
	}
	if wait > openAICodexTicketBackoffMax {
		wait = openAICodexTicketBackoffMax
	}
	b.until = now.Add(wait)
	s.openaiCodexTicketBackoff.Store(accountID, b)
	return wait
}

// clearOpenAICodexTicketBackoff 在任何非 429 的探测结果后重置：拿到票、312 miss、
// 甚至 5xx，都说明上游没有在按频率拒绝我们。
func (s *OpenAIGatewayService) clearOpenAICodexTicketBackoff(accountID int64) {
	s.openaiCodexTicketBackoff.Delete(accountID)
}

// ValidateOpenAICodexTicketTargetLength 校验后台提交的门票目标长度。
func ValidateOpenAICodexTicketTargetLength(n int) error {
	if n < openAICodexTicketMinTargetLength || n > openAICodexTicketMaxTargetLength {
		return fmt.Errorf("codex ticket target length must be between %d and %d", openAICodexTicketMinTargetLength, openAICodexTicketMaxTargetLength)
	}
	return nil
}

// ErrOpenAICodexTicketUnavailable 表示该号该模型没有可用的 292 门票，
// 且缺票拦截（fail_closed，默认关闭）禁止裸打业务请求。
var ErrOpenAICodexTicketUnavailable = errors.New("codex turn-state ticket unavailable")

type openAICodexTicket struct {
	AccountID  int64     `json:"account_id"`
	Model      string    `json:"model"`
	State      string    `json:"state"`
	Length     int       `json:"length"`
	CapturedAt time.Time `json:"captured_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Attempts   int       `json:"attempts"`
}

func openAICodexTicketKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

func openAICodexTicketExtraKey(model string) string {
	return openAICodexTicketExtraKeyPrefix + strings.TrimSpace(model)
}

func normalizeOpenAICodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

func extractOpenAICodexTicketModel(body []byte) string {
	return normalizeOpenAICodexTicketModel(gjson.GetBytes(body, "model").String())
}

// openAICodexTicketConfig 返回生效的门票配置：yaml/env 为基线，target_length 会被
// 后台热设置覆盖（enabled / fail_closed / harvest_proxy_url 有各自的读取函数）。
func (s *OpenAIGatewayService) openAICodexTicketConfig() config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	if s != nil && s.settingService != nil {
		cfg.TargetLength = s.settingService.GetOpenAICodexTicketTargetLength(context.Background(), cfg.TargetLength)
		cfg.HarvestProbeIntervalSeconds = s.settingService.GetOpenAICodexTicketHarvestProbeIntervalSeconds(context.Background(), cfg.HarvestProbeIntervalSeconds)
	}
	if cfg.TargetLength <= 0 {
		cfg.TargetLength = openAICodexTicketDefaultTargetLength
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.RefreshBeforeSeconds <= 0 {
		cfg.RefreshBeforeSeconds = 600
	}
	if cfg.HarvestProbeIntervalSeconds <= 0 {
		cfg.HarvestProbeIntervalSeconds = openAICodexTicketDefaultProbeIntervalSeconds
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 25
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	return cfg
}

func (s *OpenAIGatewayService) openAICodexTicketGatedModel(model string) bool {
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketEnabled() {
		return false
	}
	for _, item := range s.openAICodexTicketConfig().Models {
		if normalizeOpenAICodexTicketModel(item) == model {
			return true
		}
	}
	return false
}

// OpenAICodexTicketStatus 是给管理端看的门票摘要，不含 state blob。
type OpenAICodexTicketStatus struct {
	Model            string     `json:"model"`
	Length           int        `json:"length,omitempty"`
	Ready            bool       `json:"ready"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	Blocked          bool       `json:"blocked"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

func OpenAICodexTicketStatuses(account *Account, cfg config.OpenAICodexTicketConfig, now time.Time) []OpenAICodexTicketStatus {
	if !cfg.Enabled || !isOpenAICodexTicketAccount(account) {
		return nil
	}
	models, targetLen := cfg.Models, cfg.TargetLength
	if len(models) == 0 {
		models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel}
	}
	if targetLen <= 0 {
		targetLen = openAICodexTicketDefaultTargetLength
	}
	out := make([]OpenAICodexTicketStatus, 0, len(models))
	for _, model := range models {
		model = normalizeOpenAICodexTicketModel(model)
		if model == "" {
			continue
		}
		status := OpenAICodexTicketStatus{Model: model}
		ticket := parseOpenAICodexTicketFromAny(0, model, nil)
		if account != nil && account.Extra != nil {
			ticket = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
		}
		if ticket.valid(now, targetLen) {
			status.Ready = true
			status.Length = ticket.Length
			remaining := int64(ticket.ExpiresAt.Sub(now) / time.Second)
			if remaining < 0 {
				remaining = 0
			}
			status.RemainingSeconds = remaining
			exp := ticket.ExpiresAt
			status.ExpiresAt = &exp
		}
		status.Blocked = cfg.FailClosed && !status.Ready
		out = append(out, status)
	}
	return out
}

func (s *OpenAIGatewayService) openAICodexTicketEnabled() bool {
	return s.openAICodexTicketEnabledContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketEnabled(ctx, fallback)
	}
	return fallback
}

// openAICodexTicketFailClosed 返回缺票拦截开关。默认关闭：门票只是增强能力，
// 无票时不注入、不拦截，由上游决定；开启后无票账号对门控模型暂停调度。
// 后台设置优先（热更新、无需重启），缺失时回退 yaml/env。
func (s *OpenAIGatewayService) openAICodexTicketFailClosed() bool {
	return s.openAICodexTicketFailClosedContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketFailClosedContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.FailClosed
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketFailClosed(ctx, fallback)
	}
	return fallback
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURL() string {
	return s.openAICodexTicketHarvestProxyURLContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURLContext(ctx context.Context) string {
	if s.settingService != nil {
		if proxy := s.settingService.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
			return proxy
		}
	}
	return strings.TrimSpace(s.openAICodexTicketConfig().HarvestProxyURL)
}

func (t *openAICodexTicket) valid(now time.Time, targetLen int) bool {
	if t == nil {
		return false
	}
	state := strings.TrimSpace(t.State)
	if len(state) != targetLen || t.Length != targetLen || !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
		return false
	}
	if t.ExpiresAt.IsZero() || !now.Before(t.ExpiresAt) {
		return false
	}
	return true
}

func (t *openAICodexTicket) needsRefresh(now time.Time, refreshBefore time.Duration) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return true
	}
	return !t.ExpiresAt.After(now.Add(refreshBefore))
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicket(account *Account, model string) *openAICodexTicket {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" {
		return nil
	}
	key := openAICodexTicketKey(account.ID, model)
	targetLen := s.openAICodexTicketConfig().TargetLength
	now := time.Now()
	var mem *openAICodexTicket
	if raw, ok := s.openaiCodexTickets.Load(key); ok {
		mem, _ = raw.(*openAICodexTicket)
	}
	var extra *openAICodexTicket
	if account.Extra != nil {
		extra = parseOpenAICodexTicketFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	}
	if extra.valid(now, targetLen) && (mem == nil || extra.CapturedAt.After(mem.CapturedAt)) {
		s.openaiCodexTickets.Store(key, extra)
		return extra
	}
	if mem.valid(now, targetLen) {
		return mem
	}
	if extra != nil {
		s.openaiCodexTickets.Store(key, extra)
		return extra
	}
	if mem != nil {
		s.openaiCodexTickets.Delete(key)
	}
	return nil
}

func parseOpenAICodexTicketFromAny(accountID int64, model string, raw any) *openAICodexTicket {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var ticket openAICodexTicket
	if err := json.Unmarshal(b, &ticket); err != nil {
		return nil
	}
	ticket.AccountID = accountID
	if strings.TrimSpace(model) != "" {
		ticket.Model = model
	}
	ticket.State = strings.TrimSpace(ticket.State)
	if ticket.Length == 0 {
		ticket.Length = len(ticket.State)
	}
	if ticket.State == "" {
		return nil
	}
	return &ticket
}

func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) {
	if s == nil || account == nil || ticket == nil || account.ID <= 0 {
		return
	}
	model := normalizeOpenAICodexTicketModel(ticket.Model)
	ticket.Model = model
	ticket.AccountID = account.ID
	s.openaiCodexTickets.Store(openAICodexTicketKey(account.ID, model), ticket)
	if s.accountRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		openAICodexTicketExtraKey(model): ticket,
	}); err != nil {
		logger.L().Warn("openai_codex_ticket persist failed",
			zap.Int64("account_id", account.ID),
			zap.String("model", model),
			zap.Error(err),
		)
	}
}

// applyOpenAICodexTicket 在出站请求上覆盖 x-codex-turn-state。
// 请求路径只注入已捕获的有效门票，不现场打票；打票由后台 harvester 完成。
// 无票时：缺票拦截关闭（默认）→ 不碰请求头，保留客户端自带的 turn-state
// 原样转发，由上游决定；缺票拦截开启 → 返回 ErrOpenAICodexTicketUnavailable。
func (s *OpenAIGatewayService) applyOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header) error {
	_, err := s.injectOpenAICodexTicket(ctx, account, model, h)
	return err
}

// injectOpenAICodexTicket 是 applyOpenAICodexTicket 的实现，额外返回本次真正
// 注入的门票（未注入返回 nil），供守护回执绑定。
func (s *OpenAIGatewayService) injectOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header) (*openAICodexTicket, error) {
	if s == nil || h == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledContext(ctx) {
		return nil, nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	if model == "" || !s.openAICodexTicketGatedModel(model) {
		return nil, nil
	}
	cfg := s.openAICodexTicketConfig()
	ticket := s.lookupOpenAICodexTicket(account, model)
	if ticket.valid(time.Now(), cfg.TargetLength) {
		h.Set(openAICodexTurnStateHeader, ticket.State)
		return ticket, nil
	}
	if !s.openAICodexTicketFailClosedContext(ctx) {
		return nil, nil
	}
	return nil, ErrOpenAICodexTicketUnavailable
}

// ---- 无票兜底降级 ----
//
// 没票时与其让上游把 gpt-6-astra 悄悄降成 gpt-5.6-luna，不如网关自己把出站模型
// 改成 gpt-5.6-sol：sol 在生产上从未被降级。只做一级映射，兜底模型自身不再映射；
// reasoning.effort 原样继承；缺票拦截开着时不兜底（严格模式按拦截处理）。

const openAICodexTicketFallbackContextKey = "openai_codex_ticket_fallback"

// ParseOpenAICodexTicketFallbackModels 解析 "from=to" 映射，多条以换行/逗号/分号分隔。
func ParseOpenAICodexTicketFallbackModels(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ',' || r == ';' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		from, to, ok := strings.Cut(item, "=")
		if !ok {
			from, to, ok = strings.Cut(item, "→")
		}
		from, to = normalizeOpenAICodexTicketModel(from), normalizeOpenAICodexTicketModel(to)
		if !ok || from == "" || to == "" {
			return nil, fmt.Errorf("fallback mapping %q must look like from=to", item)
		}
		if from == to {
			return nil, fmt.Errorf("fallback mapping %q maps a model to itself", item)
		}
		out[from] = to
	}
	return out, nil
}

// ValidateOpenAICodexTicketFallbackModels 校验后台提交的映射原文（空串合法：回退 yaml）。
func ValidateOpenAICodexTicketFallbackModels(raw string) error {
	_, err := ParseOpenAICodexTicketFallbackModels(raw)
	return err
}

// FormatOpenAICodexTicketFallbackModels 把 yaml 映射表格式化为后台原文（稳定顺序）。
func FormatOpenAICodexTicketFallbackModels(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(m[k]) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, strings.TrimSpace(k)+"="+strings.TrimSpace(m[k]))
	}
	return strings.Join(parts, "\n")
}

func (s *OpenAIGatewayService) openAICodexTicketFallbackEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketFallbackEnabled(ctx, true)
	}
	return true
}

// openAICodexTicketFallbackModels 返回生效的兜底映射：后台原文优先（解析失败视为空），
// 空则回退 yaml。
func (s *OpenAIGatewayService) openAICodexTicketFallbackModels(ctx context.Context) map[string]string {
	if s == nil {
		return nil
	}
	if s.settingService != nil {
		if raw := strings.TrimSpace(s.settingService.GetOpenAICodexTicketFallbackModels(ctx)); raw != "" {
			if m, err := ParseOpenAICodexTicketFallbackModels(raw); err == nil {
				return m
			}
		}
	}
	if s.cfg != nil {
		m := make(map[string]string, len(s.cfg.Gateway.OpenAICodexTicket.FallbackModels))
		for k, v := range s.cfg.Gateway.OpenAICodexTicket.FallbackModels {
			if k, v = normalizeOpenAICodexTicketModel(k), normalizeOpenAICodexTicketModel(v); k != "" && v != "" && k != v {
				m[k] = v
			}
		}
		return m
	}
	return nil
}

// openAICodexTicketFallbackModel 判定本次出站是否应改写为兜底模型。
// 条件：门票功能开、兜底开、缺票拦截关、outboundModel 是门控模型、该账号该模型
// 当前没有有效门票、映射表里有它。返回 (兜底模型, true)；否则 ("", false)。
func (s *OpenAIGatewayService) openAICodexTicketFallbackModel(ctx context.Context, account *Account, outboundModel string) (string, bool) {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledContext(ctx) {
		return "", false
	}
	model := normalizeOpenAICodexTicketModel(outboundModel)
	if model == "" || !s.openAICodexTicketGatedModel(model) {
		return "", false
	}
	if !s.openAICodexTicketFallbackEnabledContext(ctx) || s.openAICodexTicketFailClosedContext(ctx) {
		return "", false
	}
	target, ok := s.openAICodexTicketFallbackModels(ctx)[model]
	if !ok || target == "" || target == model {
		return "", false
	}
	if s.lookupOpenAICodexTicket(account, model).valid(time.Now(), s.openAICodexTicketConfig().TargetLength) {
		return "", false
	}
	return target, true
}

// applyOpenAICodexTicketFallback 是各出站路径的统一入口：需要兜底时返回改写后的
// 模型、在请求上下文打标、记日志；不需要时原样返回。
func (s *OpenAIGatewayService) applyOpenAICodexTicketFallback(ctx context.Context, c *gin.Context, account *Account, outboundModel string) (string, bool) {
	target, ok := s.openAICodexTicketFallbackModel(ctx, account, outboundModel)
	if !ok {
		return outboundModel, false
	}
	if c != nil {
		c.Set(openAICodexTicketFallbackContextKey, normalizeOpenAICodexTicketModel(outboundModel)+"→"+target)
	}
	logger.L().Info("openai_codex_ticket fallback model applied",
		zap.Int64("account_id", account.ID),
		zap.String("from", normalizeOpenAICodexTicketModel(outboundModel)),
		zap.String("to", target))
	return target, true
}

// OpenAICodexTicketFallbackFromContext 返回本请求的兜底标记（"from→to"），未兜底为空。
func OpenAICodexTicketFallbackFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(openAICodexTicketFallbackContextKey); ok {
		if str, ok := v.(string); ok {
			return str
		}
	}
	return ""
}

// ---- 门票守护（watchdog）----
//
// 上游对「坏票」不报错，只是悄悄把 gpt-6-astra 按 gpt-5.6-luna 服务，并在响应头
// 带回 312 长度的 turn-state。守护在业务响应完成后读这两个信号：命中即作废本次
// 注入的那张票并触发一次重采。它不重放业务请求、不改已发出的响应。

const (
	openAICodexTicketWatchdogExtraKey       = openAICodexTicketExtraKeyPrefix + "watchdog"
	openAICodexTicketReceiptContextKey      = "openai_codex_ticket_receipt"
	openAICodexTicketWatchdogReasonModel    = "model_mismatch"
	openAICodexTicketWatchdogReasonState312 = "state_312"
	openAICodexTicketDegradedStateLength    = 312
)

// OpenAICodexTicketWatchdogStatus 是账号级守护摘要，给管理端看；不含 state blob。
type OpenAICodexTicketWatchdogStatus struct {
	TriggerCount      int64      `json:"trigger_count"`
	LastReason        string     `json:"last_reason,omitempty"`
	LastModel         string     `json:"last_model,omitempty"`
	LastResponseModel string     `json:"last_response_model,omitempty"`
	LastTriggeredAt   *time.Time `json:"last_triggered_at,omitempty"`
}

// OpenAICodexTicketWatchdogStatusOf 从账号 Extra 读取守护摘要；从未触发返回 nil。
func OpenAICodexTicketWatchdogStatusOf(account *Account) *OpenAICodexTicketWatchdogStatus {
	if account == nil || account.Extra == nil {
		return nil
	}
	raw, ok := account.Extra[openAICodexTicketWatchdogExtraKey]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var status OpenAICodexTicketWatchdogStatus
	if err := json.Unmarshal(b, &status); err != nil || status.TriggerCount <= 0 {
		return nil
	}
	return &status
}

// openAICodexTicketReceipt 记录「这次请求注入了哪张票」，绑定到注入那一刻的
// 票身份（账号、模型、捕获时间）。一个迟到的响应不能作废之后新采的票。
type openAICodexTicketReceipt struct {
	accountID  int64
	model      string
	capturedAt time.Time
}

func (r *openAICodexTicketReceipt) matches(ticket *openAICodexTicket) bool {
	return r != nil && ticket != nil && ticket.AccountID == r.accountID &&
		normalizeOpenAICodexTicketModel(ticket.Model) == r.model && ticket.CapturedAt.Equal(r.capturedAt)
}

// applyOpenAICodexTicketForRequest 在 applyOpenAICodexTicket 之上把注入回执挂到
// 下游请求上下文。failover 换号时每次尝试都会重新调用：注入则覆盖回执，未注入
// 则清除，保证回执始终对应最后一次真正出站的请求。
func (s *OpenAIGatewayService) applyOpenAICodexTicketForRequest(ctx context.Context, c *gin.Context, account *Account, model string, h http.Header) error {
	ticket, err := s.injectOpenAICodexTicket(ctx, account, model, h)
	if c != nil {
		if err == nil && ticket != nil {
			c.Set(openAICodexTicketReceiptContextKey, &openAICodexTicketReceipt{
				accountID:  ticket.AccountID,
				model:      normalizeOpenAICodexTicketModel(ticket.Model),
				capturedAt: ticket.CapturedAt,
			})
		} else {
			c.Set(openAICodexTicketReceiptContextKey, (*openAICodexTicketReceipt)(nil))
		}
	}
	return err
}

func openAICodexTicketReceiptFromContext(c *gin.Context) *openAICodexTicketReceipt {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(openAICodexTicketReceiptContextKey)
	if !ok {
		return nil
	}
	receipt, _ := raw.(*openAICodexTicketReceipt)
	return receipt
}

func (s *OpenAIGatewayService) openAICodexTicketWatchdogEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketWatchdogEnabled(ctx, true)
	}
	return true
}

// openAICodexTicketWatchdogReason 判定一次成功响应是否暴露了「坏票」信号。
func openAICodexTicketWatchdogReason(result *OpenAIForwardResult, observedModel string) (reason, responseModel string) {
	if result == nil {
		return "", ""
	}
	responseModel = strings.TrimSpace(result.UpstreamResponseModel)
	if responseModel == "" {
		responseModel = strings.TrimSpace(observedModel)
	}
	sent := upstreamSentModel(result.Model, result.UpstreamModel)
	if mismatch := upstreamModelMismatch(sent, responseModel); mismatch != nil && *mismatch && sent != "" {
		return openAICodexTicketWatchdogReasonModel, responseModel
	}
	if result.ResponseHeaders != nil {
		state := strings.TrimSpace(result.ResponseHeaders.Get(openAICodexTurnStateHeader))
		if len(state) == openAICodexTicketDegradedStateLength && strings.HasPrefix(state, openAICodexTicketStatePrefix) {
			return openAICodexTicketWatchdogReasonState312, responseModel
		}
	}
	return "", responseModel
}

// ObserveOpenAICodexTicketOutcome 在一次业务请求拿到上游结果后调用。只有本次
// 真正注入过门票（有回执）且该票仍是当前票时才会动作；命中坏票信号则作废该票、
// 落库守护摘要，并异步触发一次重采（受 429 退避约束）。
func (s *OpenAIGatewayService) ObserveOpenAICodexTicketOutcome(c *gin.Context, account *Account, result *OpenAIForwardResult) {
	if s == nil || c == nil || account == nil || result == nil {
		return
	}
	receipt := openAICodexTicketReceiptFromContext(c)
	if receipt == nil || receipt.accountID != account.ID {
		return
	}
	// 回执只用一次：同一请求后续（例如 failover 部分结果）不重复判定。
	c.Set(openAICodexTicketReceiptContextKey, (*openAICodexTicketReceipt)(nil))
	ctx := context.Background()
	if c.Request != nil {
		ctx = c.Request.Context()
	}
	if !s.openAICodexTicketEnabledContext(ctx) || !s.openAICodexTicketWatchdogEnabledContext(ctx) {
		return
	}
	reason, responseModel := openAICodexTicketWatchdogReason(result, observedUpstreamResponseModel(c))
	if reason == "" {
		return
	}
	current := s.lookupOpenAICodexTicket(account, receipt.model)
	if !receipt.matches(current) {
		return
	}
	now := time.Now()
	key := openAICodexTicketKey(account.ID, receipt.model)
	s.openaiCodexTickets.Delete(key)
	status := OpenAICodexTicketWatchdogStatusOf(account)
	if status == nil {
		status = &OpenAICodexTicketWatchdogStatus{}
	}
	status.TriggerCount++
	status.LastReason = reason
	status.LastModel = receipt.model
	status.LastResponseModel = responseModel
	status.LastTriggeredAt = &now
	if account.Extra != nil {
		delete(account.Extra, openAICodexTicketExtraKey(receipt.model))
		account.Extra[openAICodexTicketWatchdogExtraKey] = status
	}
	logger.L().Warn("openai_codex_ticket watchdog invalidated ticket",
		zap.Int64("account_id", account.ID), zap.String("model", receipt.model),
		zap.String("reason", reason), zap.String("response_model", responseModel),
		zap.Int64("trigger_count", status.TriggerCount))
	if s.accountRepo != nil {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.accountRepo.UpdateExtra(persistCtx, account.ID, map[string]any{
			openAICodexTicketExtraKey(receipt.model): nil,
			openAICodexTicketWatchdogExtraKey:        status,
		}); err != nil {
			logger.L().Warn("openai_codex_ticket watchdog persist failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}
	}
	// 立即补一发探测；singleflight 去重，429 退避窗口内 probeOnce 自己会跳过。
	acc := *account
	acc.Extra = maps.Clone(account.Extra)
	acc.Credentials = maps.Clone(account.Credentials)
	go s.probeOnceOpenAICodexTicket(context.Background(), &acc, receipt.model)
}

// openAICodexTicketOutboundModel 预测本请求真正出站的模型名，也就是
// applyOpenAICodexTicket 注入时读到的 body.model。
//
// 调度门控与注入必须按同一个模型名判定门票。普通请求下二者同源：Forward 的
// upstreamModel 与本函数都走 resolveOpenAIAccountUpstreamModelForRequest，且
// Forward 会把 body.model 改写成该值后才注入。但 /responses/compact 例外——
// Forward 会把出站模型进一步改写为 compact 映射或 gateway.openai_compact_model
// （默认非空），此时若门控仍按客户端原始模型判定，就会把「实际出站是非门控
// 模型、根本不需要票」的 compact 请求整片误拦成不可调度。
func (s *OpenAIGatewayService) openAICodexTicketOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		// 与 Forward 同序：compact 兜底模型优先于普通/compact 映射结果。
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}

// outboundModel 必须是真正会发给上游的模型名（openAICodexTicketOutboundModel），
// 不是客户端原始模型：注入侧读的是出站 body.model，两侧口径必须一致。
// 只有缺票拦截开启时才会把无票账号判为不可调度。
func (s *OpenAIGatewayService) openAICodexTicketBlocksAccount(account *Account, outboundModel string) bool {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() {
		return false
	}
	if !s.openAICodexTicketFailClosed() {
		return false
	}
	cfg := s.openAICodexTicketConfig()
	model := normalizeOpenAICodexTicketModel(outboundModel)
	if !s.openAICodexTicketGatedModel(model) {
		return false
	}
	ticket := s.lookupOpenAICodexTicket(account, model)
	return !ticket.valid(time.Now(), cfg.TargetLength)
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, attemptTimeout time.Duration) (state string, status int, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()

	body := []byte(`{"model":` + jsonString(model) + `,"store":false,"stream":true,"instructions":"Reply with exactly: pong","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest))
	req.Close = true
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(attemptCtx, s.accountRepo, req.Header, account); err != nil {
		return "", 0, err
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)

	// Synthetic probes must use the dedicated no-reuse transport even when the
	// production account is bound to a plugin. This also avoids reading pluginManager
	// while handlers are still wiring it during gateway construction.
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return "", 0, err
	}
	if resp == nil {
		return "", 0, errors.New("nil upstream response")
	}
	// Only the response header is needed; no connection will be reused.
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	return extractOpenAICodexTurnState(resp.Header), resp.StatusCode, nil
}

func jsonString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func applyOpenAICodexTicketHarvestIdentity(h http.Header, model string) {
	ensureCodexIdentityHeaders(h)
	enforceCodexIdentityHeaders(h)
	version := strings.TrimSpace(h.Get("version"))
	if needsOpenAICodexAstraVersion(model) && (version == "" || CompareVersions(version, openAICodexAstraMinVersion) < 0) {
		h.Set("version", openAICodexAstraMinVersion)
		h.Set("user-agent", buildCodexCLIUserAgent(openAICodexAstraMinVersion))
		h.Set("originator", openai.CodexDefaultOriginator)
	}
}

func needsOpenAICodexAstraVersion(model string) bool {
	m := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return strings.Contains(m, "gpt-6") || strings.Contains(m, "astra")
}

func (s *OpenAIGatewayService) StartOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.openaiCodexTicketCancel = cancel
	s.openaiCodexTicketDone = done
	go func() {
		defer close(done)
		s.openAICodexTicketHarvestLoop(ctx)
	}()
	logger.L().Info("openai_codex_ticket harvester started",
		zap.Int("ttl_seconds", s.openAICodexTicketConfig().TTLSeconds),
		zap.Int("target_length", s.openAICodexTicketConfig().TargetLength),
		zap.Int("probe_interval_seconds", s.openAICodexTicketConfig().HarvestProbeIntervalSeconds),
		zap.Strings("models", s.openAICodexTicketConfig().Models),
	)
}

func (s *OpenAIGatewayService) StopOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	s.openaiCodexTicketStopped = true
	cancel, done := s.openaiCodexTicketCancel, s.openaiCodexTicketDone
	s.openaiCodexTicketLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.refreshOpenAICodexTickets(ctx)
			timer.Reset(time.Duration(s.openAICodexTicketConfig().HarvestProbeIntervalSeconds) * time.Second)
		}
	}
}

// refreshOpenAICodexTickets probes each account/model with a missing or soon-to-expire
// ticket once. The loop waits for all probes, then waits the configured interval
// before starting the next cycle.
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		logger.L().Warn("openai_codex_ticket list accounts failed", zap.Error(err))
		return
	}
	cfg := s.openAICodexTicketConfig()
	now := time.Now()
	refreshBefore := time.Duration(cfg.RefreshBeforeSeconds) * time.Second
	var wg sync.WaitGroup
	probed := 0
	for i := range accounts {
		account := accounts[i]
		if account.Status != StatusActive || !isOpenAICodexTicketAccount(&account) {
			continue
		}
		// 上游按账号限流：该账号处于 429 退避窗口内则本周期整体跳过，不再叠加请求。
		if b := s.openAICodexTicketBackoffFor(account.ID); now.Before(b.until) {
			logger.L().Info("openai_codex_ticket probe backoff",
				zap.Int64("account_id", account.ID),
				zap.Int("consecutive_429", b.consecutive429),
				zap.Int64("remaining_seconds", int64(b.until.Sub(now)/time.Second)))
			continue
		}
		for _, model := range cfg.Models {
			model := normalizeOpenAICodexTicketModel(model)
			if model == "" {
				continue
			}
			// 已有一张有效且未临近过期的票 → 本周期不打，省得白刷。
			if t := s.lookupOpenAICodexTicket(&account, model); t.valid(now, cfg.TargetLength) && !t.needsRefresh(now, refreshBefore) {
				continue
			}
			acc := account
			// Token/header helpers may update account metadata; each model owns its maps.
			acc.Extra = maps.Clone(account.Extra)
			acc.Credentials = maps.Clone(account.Credentials)
			probed++
			wg.Add(1)
			go func(acc Account, model string) {
				defer wg.Done()
				s.probeOnceOpenAICodexTicket(ctx, &acc, model)
			}(acc, model)
		}
	}
	wg.Wait()
	if probed > 0 {
		logger.L().Info("openai_codex_ticket probe cycle", zap.Int("probed", probed))
	}
}

// probeOnceOpenAICodexTicket 走打票代理打一发。命中合格 292（HTTP 200、长度==target、
// gAAAAA 前缀）就落库；否则记 Info miss，交给下个周期重试。同一 key 并发去重，避免上一发还没
// 回来又叠一发。
func (s *OpenAIGatewayService) probeOnceOpenAICodexTicket(ctx context.Context, account *Account, model string) {
	if s == nil || !isOpenAICodexTicketAccount(account) || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return
	}
	cfg := s.openAICodexTicketConfig()
	proxyURL := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if proxyURL == "" || s.httpUpstream == nil || ctx.Err() != nil {
		return
	}
	if b := s.openAICodexTicketBackoffFor(account.ID); time.Now().Before(b.until) {
		return
	}
	key := openAICodexTicketKey(account.ID, model)
	_, _, _ = s.openaiCodexTicketFlight.Do(key, func() (any, error) {
		token, _, err := s.GetAccessToken(ctx, account)
		if err != nil || strings.TrimSpace(token) == "" {
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("reason", "token"), zap.Error(err))
			return nil, nil
		}
		state, status, perr := s.fireOpenAICodexTicketProbe(ctx, account, token, model, proxyURL, time.Duration(cfg.HarvestAttemptTimeoutSeconds)*time.Second)
		if perr != nil {
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.String("reason", "error"), zap.Error(perr))
			return nil, nil
		}
		if status == http.StatusTooManyRequests {
			wait := s.noteOpenAICodexTicketProbe429(account.ID, time.Now(), time.Duration(cfg.HarvestProbeIntervalSeconds)*time.Second)
			logger.L().Warn("openai_codex_ticket probe rate limited",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.Int("consecutive_429", s.openAICodexTicketBackoffFor(account.ID).consecutive429),
				zap.Int64("backoff_seconds", int64(wait/time.Second)))
			return nil, nil
		}
		s.clearOpenAICodexTicketBackoff(account.ID)
		if status != http.StatusOK || state == "" || len(state) != cfg.TargetLength || !strings.HasPrefix(state, openAICodexTicketStatePrefix) {
			logger.L().Info("openai_codex_ticket probe miss",
				zap.Int64("account_id", account.ID), zap.String("model", model),
				zap.Int("http", status), zap.Int("len", len(state)))
			return nil, nil
		}
		now := time.Now()
		ticket := &openAICodexTicket{
			AccountID:  account.ID,
			Model:      model,
			State:      state,
			Length:     len(state),
			CapturedAt: now,
			ExpiresAt:  now.Add(time.Duration(cfg.TTLSeconds) * time.Second),
			Attempts:   1,
		}
		s.storeOpenAICodexTicket(ctx, account, ticket)
		logger.L().Info("openai_codex_ticket harvested",
			zap.Int64("account_id", account.ID), zap.String("model", model),
			zap.Int("length", ticket.Length), zap.String("mode", "continuous"))
		return nil, nil
	})
}

// IsOpenAICodexTicketExtraKey identifies server-managed ticket material.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// MergeOpenAICodexTicketExtra preserves only persisted tickets, never summaries or
// blobs supplied by an account edit. The repository repeats this under the row
// lock so a concurrent harvest cannot be overwritten by a stale admin snapshot.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for key := range result {
		if IsOpenAICodexTicketExtraKey(key) {
			delete(result, key)
		}
	}
	for key, value := range current {
		if IsOpenAICodexTicketExtraKey(key) {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// ValidateOpenAICodexTicketHarvestProxyURL validates only syntax, without making
// a network request or including credentials in validation errors.
func ValidateOpenAICodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}

// MaskProxyURL never returns a stored proxy password, even for invalid legacy data.
func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || ValidateOpenAICodexTicketHarvestProxyURL(raw) != nil {
		return ""
	}
	parsed, _ := url.Parse(raw)
	if parsed.User != nil {
		if _, ok := parsed.User.Password(); ok {
			parsed.User = url.UserPassword(parsed.User.Username(), "***")
		}
	}
	return parsed.String()
}

// IsMaskedProxyURL recognizes the exact password placeholder emitted by the API.
func IsMaskedProxyURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return false
	}
	password, ok := parsed.User.Password()
	return ok && password == "***"
}

// Credential shadows do not own tickets. Keep their existing forwarding policy
// instead of imposing a gate for a key the harvester never populates.
func isOpenAICodexTicketAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// IsOpenAICodexTicketPrivateExtraKey also covers the retired account-level proxy
// override, whose credentials may remain in older account records.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// RedactOpenAICodexTicketExtra strips ephemeral ticket material from exports
// without changing the source account or unrelated backup fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}
