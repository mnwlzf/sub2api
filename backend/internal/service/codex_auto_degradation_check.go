package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// CodexDegradationCheckExtraKey 是 accounts.extra 中保存本功能每账号调度
	// 状态的 key：{"status","checked_at","next_check_at","blocked_until",
	// "models","reason","probe_results"}。
	CodexDegradationCheckExtraKey = "codex_degradation_check"

	// CodexDegradationBlockReasonSource 是写入 temp_unschedulable_reason 的
	// 机器可读标记。只有携带该 source 的封锁才会被本功能自动解除，503 临时
	// 停调规则等其它子系统写入的封锁必须原样存活。
	CodexDegradationBlockReasonSource = "codex_degradation_check"

	codexDegradationDefaultIntervalMinutes = 10
	codexDegradationDefaultBlockMinutes    = 30
	// 运行器扫描周期：以分钟粒度发现到期账号，具体间隔由设置决定。
	codexDegradationCycleInterval = time.Minute
	// 单轮最多处理的账号数：探测是账号原生的完整 ModelTrace 请求，顺序执行
	// 且每发有独立超时，必须限制单轮工作量。
	codexDegradationMaxPerRun = 8
	// 单发探测超时，与手动降智检测入口保持一致（120s）。
	codexDegradationProbeTimeout = 120 * time.Second
	// 单轮运行上下文上限：maxPerRun × probeTimeout 之外再留余量；Stop() 取消
	// 父上下文时同样立刻生效，绝不泄漏 goroutine。
	codexDegradationRunTimeout = 20 * time.Minute

	CodexDegradationStatusNormal    = "normal"
	CodexDegradationStatusDegraded  = "degraded"
	CodexDegradationStatusUncertain = "uncertain"
	CodexDegradationStatusFailed    = "failed"
)

// CodexDegradationSettings 控制自动降智检测后台运行器。
// 默认关闭，避免意外产生上游探测流量。
type CodexDegradationSettings struct {
	Enabled         bool     `json:"enabled"`
	IntervalMinutes int      `json:"interval_minutes"`
	BlockMinutes    int      `json:"block_minutes"`
	Models          []string `json:"models"`
}

// DefaultCodexDegradationSettings 返回缺省配置：关闭、10 分钟一检、降智封锁
// 30 分钟、默认检测模型 gpt-6.1-sol。
func DefaultCodexDegradationSettings() CodexDegradationSettings {
	return CodexDegradationSettings{
		Enabled:         false,
		IntervalMinutes: codexDegradationDefaultIntervalMinutes,
		BlockMinutes:    codexDegradationDefaultBlockMinutes,
		Models:          []string{"gpt-6.1-sol"},
	}
}

// ValidateCodexDegradationSettings 是保存路径的校验入口：
// interval/block ≥ 1；models 必须非空、无重复，且都是 ModelTraceDiagnosticModels()
// 的子集——指纹库换代后失效的模型在保存时直接拒绝并给出明确报错。
func ValidateCodexDegradationSettings(settings *CodexDegradationSettings) error {
	if settings == nil {
		return infraerrors.BadRequest("INVALID_CODEX_DEGRADATION_SETTINGS", "settings cannot be nil")
	}
	if settings.IntervalMinutes < 1 {
		return infraerrors.BadRequest("INVALID_CODEX_DEGRADATION_INTERVAL", "interval_minutes must be at least 1")
	}
	if settings.BlockMinutes < 1 {
		return infraerrors.BadRequest("INVALID_CODEX_DEGRADATION_BLOCK", "block_minutes must be at least 1")
	}
	if len(settings.Models) == 0 {
		return infraerrors.BadRequest("INVALID_CODEX_DEGRADATION_MODELS", "models must not be empty")
	}
	allowed, err := ModelTraceDiagnosticModels()
	if err != nil {
		return fmt.Errorf("load diagnostic model list: %w", err)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, model := range allowed {
		allowedSet[model] = struct{}{}
	}
	seen := make(map[string]struct{}, len(settings.Models))
	for _, model := range settings.Models {
		if _, ok := allowedSet[model]; !ok {
			return infraerrors.BadRequest(
				"INVALID_CODEX_DEGRADATION_MODELS",
				fmt.Sprintf("unknown diagnostic model %q", model),
			)
		}
		if _, dup := seen[model]; dup {
			return infraerrors.BadRequest(
				"INVALID_CODEX_DEGRADATION_MODELS",
				fmt.Sprintf("duplicate diagnostic model %q", model),
			)
		}
		seen[model] = struct{}{}
	}
	return nil
}

// normalizeCodexDegradationSettings 把缺省/非法字段逐项回退默认值，保证运行器
// 永远拿到可用配置。
func normalizeCodexDegradationSettings(settings *CodexDegradationSettings) {
	defaults := DefaultCodexDegradationSettings()
	if settings.IntervalMinutes < 1 {
		settings.IntervalMinutes = defaults.IntervalMinutes
	}
	if settings.BlockMinutes < 1 {
		settings.BlockMinutes = defaults.BlockMinutes
	}
	if len(settings.Models) == 0 {
		settings.Models = defaults.Models
	}
}

// CodexDegradationProbeResult 保留请求模型与该次探测的实际判定。
// 未获得预测时省略预测模型和概率，解析失败仍可保留实际已解析数量。
type CodexDegradationProbeResult struct {
	Model          string   `json:"model"`
	Status         string   `json:"status"`
	PredictedModel string   `json:"predicted_model,omitempty"`
	Probability    *float64 `json:"probability,omitempty"`
	ParsedCount    *int     `json:"parsed_count,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	HTTPStatus     int      `json:"http_status,omitempty"`
}

// CodexDegradationCheckState 是保存在 accounts.extra 中的每账号调度状态。
type CodexDegradationCheckState struct {
	Status       string                        `json:"status"`
	CheckedAt    time.Time                     `json:"checked_at"`
	NextCheckAt  time.Time                     `json:"next_check_at"`
	BlockedUntil *time.Time                    `json:"blocked_until,omitempty"`
	Models       []string                      `json:"models"`
	Reason       string                        `json:"reason,omitempty"`
	ProbeResults []CodexDegradationProbeResult `json:"probe_results,omitempty"`
}

// decodeCodexDegradationCheckState 兼容两种载体：DB 读回的 map（JSON）与测试/
// 内存路径直接写入的结构体指针；任何解析失败都视作"无状态"（首次运行）。
func decodeCodexDegradationCheckState(extra map[string]any) *CodexDegradationCheckState {
	if extra == nil {
		return nil
	}
	raw, ok := extra[CodexDegradationCheckExtraKey]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case *CodexDegradationCheckState:
		return typed
	case CodexDegradationCheckState:
		return &typed
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var state CodexDegradationCheckState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}

// codexDegradationBlockReasonPayload 是写入 temp_unschedulable_reason 的机器
// 可读封锁原因。source 是本功能的识别标记；until_unix/error_message 与既有
// TempUnsched 展示链路（tempUnschedStateFromStoredReason）字段对齐。
type codexDegradationBlockReasonPayload struct {
	Source       string                        `json:"source"`
	Status       string                        `json:"status"`
	Models       []string                      `json:"models"`
	ProbeResults []CodexDegradationProbeResult `json:"probe_results,omitempty"`
	UntilUnix    int64                         `json:"until_unix"`
	ErrorMessage string                        `json:"error_message"`
}

func buildCodexDegradationBlockReason(status string, models []string, until time.Time, results []CodexDegradationProbeResult) string {
	message := fmt.Sprintf("codex degradation check: %s", status)
	var details []string
	for _, result := range results {
		if result.Status != CodexDegradationStatusDegraded || result.PredictedModel == "" {
			continue
		}
		detail := result.Model + " → " + result.PredictedModel
		if result.Probability != nil {
			detail += fmt.Sprintf(" (%.2f%%)", *result.Probability*100)
		}
		details = append(details, detail)
	}
	if len(details) > 0 {
		message += ": " + strings.Join(details, "; ")
	}
	payload := codexDegradationBlockReasonPayload{
		Source:       CodexDegradationBlockReasonSource,
		Status:       status,
		Models:       models,
		ProbeResults: results,
		UntilUnix:    until.UTC().Unix(),
		ErrorMessage: message,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return BuildTempUnschedReasonPayload(CodexDegradationBlockReasonSource, message)
	}
	return string(raw)
}

// IsCodexDegradationBlockReason 报告一条 temp_unschedulable_reason 是否由本功能
// 写入。只有带标记的封锁才允许被健康探测自动解除。
func IsCodexDegradationBlockReason(rawReason string) bool {
	payload, ok := parseTempUnschedReasonPayload(rawReason)
	return ok && payload.Source == CodexDegradationBlockReasonSource
}

// CodexDegradationDueAccountLister 是自动检测发现"到期账号"的窄接口，
// 由 repository.accountRepository 实现；未实现时回退 ListByPlatform 全量过滤。
type CodexDegradationDueAccountLister interface {
	ListDueCodexDegradationCheckAccounts(ctx context.Context, now time.Time, limit int) ([]Account, error)
}

// CodexDegradationProbeResultStore 是探测结果落库的窄接口（可选依赖）。
type CodexDegradationProbeResultStore interface {
	InsertCodexDegradationProbeResult(ctx context.Context, record CodexDegradationProbeResultRecord) error
	ListCodexDegradationProbeResults(ctx context.Context, accountID int64, limit int) ([]CodexDegradationProbeResultRecord, error)
}

// CodexDegradationProbeResultRecord 是一条持久化的探测结果。
type CodexDegradationProbeResultRecord struct {
	AccountID      int64     `json:"account_id"`
	Model          string    `json:"model"`
	Status         string    `json:"status"`
	Reason         string    `json:"reason,omitempty"`
	PredictedModel string    `json:"predicted_model,omitempty"`
	Probability    float64   `json:"probability"`
	ParsedCount    int       `json:"parsed_count"`
	HTTPStatus     int       `json:"http_status"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"created_at"`
}

// codexDegradationProbe 以账号自身的上游凭据执行一发降智探测并给出判定。
// 抽成接口是为了让状态机测试不触网。
type codexDegradationProbe interface {
	ProbeCodexDegradation(ctx context.Context, accountID int64, model string) (CodexDiagnosticVerdict, error)
}

// ProbeCodexDegradation 是生产实现：账号原生 ModelTrace 探测 + 统一评分。
func (s *CodexDegradationService) ProbeCodexDegradation(ctx context.Context, accountID int64, model string) (CodexDiagnosticVerdict, error) {
	challenge, err := NewModelTraceChallenge()
	if err != nil {
		return CodexDiagnosticVerdict{Status: CodexDegradationStatusFailed}, fmt.Errorf("generate model trace challenge: %w", err)
	}
	result := s.RunNativeDiagnostic(ctx, accountID, model, challenge, s.probeTimeout)
	if result.Reason != "" {
		// 请求/HTTP/输出不可评分：按探测失败处理，绝不能当作降智或健康。
		return CodexDiagnosticVerdict{Status: CodexDegradationStatusFailed, Reason: result.Reason, HTTPStatus: result.HTTPStatus}, nil
	}
	return EvaluateCodexDiagnostic(model, result.Output, challenge), nil
}

// Start 启动自动降智检测后台运行器。默认设置关闭时循环仍会运行，但每轮
// RunDue 会立即返回，不产生任何探测流量。重复调用安全。
func (s *CodexDegradationService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.parentCtx, s.parentCancel = context.WithCancel(ctx)
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.runLoop(s.parentCtx)
}

// Stop 取消后台运行器并等待 goroutine 退出。重复调用安全。
func (s *CodexDegradationService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cancel := s.parentCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *CodexDegradationService) runLoop(ctx context.Context) {
	defer s.wg.Done()
	_ = s.RunDue(ctx)
	ticker := time.NewTicker(codexDegradationCycleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.RunDue(ctx); err != nil {
				s.logWarn("run_due_failed", "err", err)
			}
		}
	}
}

// RunDue 执行一轮到期账号检查；全程顺序执行，配合 cycleMu 保证同一账号任何
// 时刻至多一发探测在飞。
func (s *CodexDegradationService) RunDue(ctx context.Context) error {
	if s == nil || s.accountRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.cycleMu.Lock()
	defer s.cycleMu.Unlock()

	settings, err := s.getSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.Enabled {
		return nil
	}
	now := s.currentTime()
	accounts, err := s.listDueAccounts(ctx, now)
	if err != nil {
		return fmt.Errorf("list due codex degradation accounts: %w", err)
	}
	due := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := accounts[i]
		if !codexDegradationEligible(&account) || !account.IsActive() {
			continue
		}
		state := decodeCodexDegradationCheckState(account.Extra)
		if state != nil && !state.NextCheckAt.IsZero() && now.Before(state.NextCheckAt) {
			continue
		}
		due = append(due, account)
	}
	sort.SliceStable(due, func(i, j int) bool {
		left := decodeCodexDegradationCheckState(due[i].Extra)
		right := decodeCodexDegradationCheckState(due[j].Extra)
		leftUnset := left == nil || left.NextCheckAt.IsZero()
		rightUnset := right == nil || right.NextCheckAt.IsZero()
		if leftUnset && rightUnset {
			return due[i].ID < due[j].ID
		}
		if leftUnset {
			return true
		}
		if rightUnset {
			return false
		}
		return left.NextCheckAt.Before(right.NextCheckAt)
	})
	if len(due) > codexDegradationMaxPerRun {
		due = due[:codexDegradationMaxPerRun]
	}

	runCtx, cancel := context.WithTimeout(ctx, codexDegradationRunTimeout)
	defer cancel()
	for i := range due {
		if runCtx.Err() != nil {
			break
		}
		if err := s.runAccountCheck(runCtx, due[i].ID, settings); err != nil {
			s.logWarn("account_check_failed", "account_id", due[i].ID, "err", err)
		}
	}
	return nil
}

// codexDegradationEligible 即诊断面板使用的账号口径：
// OpenAI 平台 + oauth/setup-token 类型。
func codexDegradationEligible(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike()
}

func (s *CodexDegradationService) listDueAccounts(ctx context.Context, now time.Time) ([]Account, error) {
	if lister, ok := s.accountRepo.(CodexDegradationDueAccountLister); ok {
		return lister.ListDueCodexDegradationCheckAccounts(ctx, now, codexDegradationMaxPerRun*4)
	}
	// 回退：仓储尚未实现 SQL 到期查询时，按平台全量拉取后在内存过滤。
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	return accounts, nil
}

func (s *CodexDegradationService) getSettings(ctx context.Context) (*CodexDegradationSettings, error) {
	defaults := s.defaultSettings()
	if s.settingService == nil {
		return &defaults, nil
	}
	return s.settingService.GetCodexDegradationSettings(ctx)
}

func (s *CodexDegradationService) defaultSettings() CodexDegradationSettings {
	if s.defaults != nil {
		settings := *s.defaults
		normalizeCodexDegradationSettings(&settings)
		return settings
	}
	return DefaultCodexDegradationSettings()
}

func (s *CodexDegradationService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

// runAccountCheck 对单个账号执行一次完整的"探测 → 判定 → 状态迁移"。
// 状态机约定：
//   - 尚未到期（含未来 next_check_at）→ 直接跳过，不产生任何写入；
//   - degraded → SetTempUnschedulable(now+block_minutes, 带标记 reason)，
//     next_check_at = now+block_minutes，复查时仍降级则续期；
//   - normal/uncertain → 若当前封锁 reason 带本功能标记则解除，
//     next_check_at = now+interval_minutes；
//   - failed（请求/HTTP/评分失败）→ 不封锁、不解封，next_check_at = now+interval_minutes。
func (s *CodexDegradationService) runAccountCheck(ctx context.Context, accountID int64, settings *CodexDegradationSettings) error {
	if s == nil || s.accountRepo == nil || settings == nil {
		return nil
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return fmt.Errorf("load account: %w", err)
	}
	if account == nil || !codexDegradationEligible(account) || !account.IsActive() {
		return nil
	}
	now := s.currentTime().UTC()
	previous := decodeCodexDegradationCheckState(account.Extra)
	if previous != nil && !previous.NextCheckAt.IsZero() && now.Before(previous.NextCheckAt) {
		return nil
	}
	models := append([]string(nil), settings.Models...)
	if len(models) == 0 {
		models = s.defaultSettings().Models
	}
	interval := time.Duration(settings.IntervalMinutes) * time.Minute

	status, reason, results := s.probeModels(ctx, accountID, models, "auto")
	state := &CodexDegradationCheckState{
		Status:       status,
		CheckedAt:    now,
		Models:       models,
		Reason:       reason,
		ProbeResults: results,
	}
	if previous != nil {
		state.BlockedUntil = previous.BlockedUntil
	}

	switch status {
	case CodexDegradationStatusDegraded:
		until := now.Add(time.Duration(settings.BlockMinutes) * time.Minute)
		// SetTempUnschedulable 是单调只延长：已有更晚的封锁（例如 503 规则的
		// 长封锁）不会被缩短，reason 也不会被覆盖，标记语义保持成立。
		if err := s.accountRepo.SetTempUnschedulable(ctx, accountID, until, buildCodexDegradationBlockReason(status, models, until, results)); err != nil {
			return fmt.Errorf("set temp unschedulable: %w", err)
		}
		state.BlockedUntil = &until
		state.NextCheckAt = until
	case CodexDegradationStatusFailed:
		// 探测失败绝不封锁、也绝不解除任何封锁。
		state.NextCheckAt = now.Add(interval)
	default: // normal / uncertain
		state.NextCheckAt = now.Add(interval)
		if account.TempUnschedulableUntil != nil && IsCodexDegradationBlockReason(account.TempUnschedulableReason) {
			if err := s.clearOwnBlock(ctx, accountID); err != nil {
				return fmt.Errorf("clear temp unschedulable: %w", err)
			}
			state.BlockedUntil = nil
		}
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{CodexDegradationCheckExtraKey: state})
}

// clearOwnBlock 只解除本功能写入的封锁。优先走 RateLimitService，保证 Redis
// 临时停调缓存与模型级限流同步清理；未注入时回退仓储直清。
func (s *CodexDegradationService) clearOwnBlock(ctx context.Context, accountID int64) error {
	if s.rateLimiter != nil {
		return s.rateLimiter.ClearTempUnschedulable(ctx, accountID)
	}
	return s.accountRepo.ClearTempUnschedulable(ctx, accountID)
}

// probeModels 顺序探测全部配置模型并合并判定：
// 任一 degraded → degraded；否则任一 failed → failed；否则任一 uncertain →
// uncertain（与 normal 同属健康）；全 normal → normal。
func (s *CodexDegradationService) probeModels(ctx context.Context, accountID int64, models []string, source string) (string, string, []CodexDegradationProbeResult) {
	results := make([]CodexDegradationProbeResult, 0, len(models))
	var degraded []string
	var failures []string
	uncertain := false
	for _, model := range models {
		var verdict CodexDiagnosticVerdict
		switch {
		case ctx.Err() != nil:
			verdict = CodexDiagnosticVerdict{Status: CodexDegradationStatusFailed, Reason: probeFailureMessage(ctx.Err())}
		case s == nil || s.probe == nil:
			verdict = CodexDiagnosticVerdict{Status: CodexDegradationStatusFailed, Reason: "probe_unavailable"}
		default:
			var err error
			verdict, err = s.probe.ProbeCodexDegradation(ctx, accountID, model)
			if err != nil {
				verdict = CodexDiagnosticVerdict{Status: CodexDegradationStatusFailed, Reason: probeFailureMessage(err)}
			}
		}
		result := CodexDegradationProbeResult{
			Model:          model,
			Status:         verdict.Status,
			PredictedModel: verdict.PredictedModel,
			Reason:         verdict.Reason,
			HTTPStatus:     verdict.HTTPStatus,
		}
		if verdict.PredictedModel != "" {
			probability := verdict.Probability
			result.Probability = &probability
		}
		if verdict.PredictedModel != "" || verdict.ParsedCount > 0 {
			parsed := verdict.ParsedCount
			result.ParsedCount = &parsed
		}
		switch verdict.Status {
		case CodexDegradationStatusDegraded:
			degraded = append(degraded, model)
		case CodexDegradationStatusUncertain:
			uncertain = true
		case CodexDegradationStatusNormal:
		default:
			result.Status = CodexDegradationStatusFailed
			if result.Reason == "" {
				result.Reason = CodexDegradationStatusFailed
			}
			failures = append(failures, fmt.Sprintf("%s: %s", model, result.Reason))
		}
		results = append(results, result)
		s.recordProbeResult(ctx, accountID, result, source)
	}
	if len(degraded) > 0 {
		return CodexDegradationStatusDegraded, "degraded models: " + strings.Join(degraded, ","), results
	}
	if len(failures) > 0 {
		return CodexDegradationStatusFailed, strings.Join(failures, ";"), results
	}
	if uncertain {
		return CodexDegradationStatusUncertain, "", results
	}
	return CodexDegradationStatusNormal, "", results
}

func probeFailureMessage(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "probe_failed"
	}
	return message
}

// recordProbeResult 尽力把探测结果落库；仓储未实现或写入失败都不影响判定。
func (s *CodexDegradationService) recordProbeResult(ctx context.Context, accountID int64, result CodexDegradationProbeResult, source string) {
	if s == nil || s.results == nil {
		return
	}
	record := CodexDegradationProbeResultRecord{
		AccountID:      accountID,
		Model:          result.Model,
		Status:         result.Status,
		Reason:         result.Reason,
		PredictedModel: result.PredictedModel,
		ParsedCount:    0,
		HTTPStatus:     result.HTTPStatus,
		Source:         source,
		CreatedAt:      s.currentTime().UTC(),
	}
	if result.Probability != nil {
		record.Probability = *result.Probability
	}
	if result.ParsedCount != nil {
		record.ParsedCount = *result.ParsedCount
	}
	if err := s.results.InsertCodexDegradationProbeResult(ctx, record); err != nil {
		s.logWarn("record_probe_result_failed", "account_id", accountID, "model", result.Model, "err", err)
	}
}

// CodexDegradationCheckResult 是一次手动降智检测的汇总结果。
// Status 四态：normal / degraded / uncertain / failed。
type CodexDegradationCheckResult struct {
	AccountID int64                         `json:"account_id"`
	Status    string                        `json:"status"`
	Reason    string                        `json:"reason,omitempty"`
	CheckedAt time.Time                     `json:"checked_at"`
	Models    []string                      `json:"models"`
	Items     []CodexDegradationProbeResult `json:"items"`
}

// CheckAccount 用账号自身凭据逐个模型执行原生探测并汇总四态判定。
// 这是管理面手动检测入口：只读上游、不写封锁（封锁只由自动检测状态机产生）。
func (s *CodexDegradationService) CheckAccount(ctx context.Context, accountID int64, models []string) (CodexDegradationCheckResult, error) {
	result := CodexDegradationCheckResult{AccountID: accountID, Status: CodexDegradationStatusFailed, CheckedAt: s.currentTime().UTC()}
	if s == nil || s.accountRepo == nil {
		return result, errors.New("codex degradation service unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(models) == 0 {
		models = s.defaultSettings().Models
	}
	allowed, err := ModelTraceDiagnosticModels()
	if err != nil {
		return result, err
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, model := range allowed {
		allowedSet[model] = struct{}{}
	}
	seen := make(map[string]struct{}, len(models))
	validated := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := allowedSet[model]; !ok {
			return result, infraerrors.BadRequest("INVALID_CODEX_DEGRADATION_MODEL", fmt.Sprintf("unknown diagnostic model %q", model))
		}
		if _, dup := seen[model]; dup {
			continue
		}
		seen[model] = struct{}{}
		validated = append(validated, model)
	}
	if len(validated) == 0 {
		return result, infraerrors.BadRequest("INVALID_CODEX_DEGRADATION_MODEL", "models must not be empty")
	}
	result.Models = validated
	result.Status, result.Reason, result.Items = s.probeModels(ctx, accountID, validated, "manual")
	return result, nil
}

// ListProbeResults 返回账号最近的探测结果；未接入结果存储时返回空列表。
func (s *CodexDegradationService) ListProbeResults(ctx context.Context, accountID int64, limit int) ([]CodexDegradationProbeResultRecord, error) {
	if s == nil || s.results == nil {
		return []CodexDegradationProbeResultRecord{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.results.ListCodexDegradationProbeResults(ctx, accountID, limit)
}

func (s *CodexDegradationService) logWarn(message string, args ...any) {
	fields := ""
	if len(args) > 0 {
		parts := make([]string, 0, len(args)/2)
		for i := 0; i+1 < len(args); i += 2 {
			parts = append(parts, fmt.Sprintf("%v=%v", args[i], args[i+1]))
		}
		fields = " " + strings.Join(parts, " ")
	}
	logger.LegacyPrintf("service.codex_degradation", "%s%s", message, fields)
}
