package service

import (
	"context"
	"encoding/json"
	"time"
)

// 本文件定义 Codex 打票（x-codex-turn-state 票据）在服务层暴露的类型与仓储端口。
// 实现拆分为：
//   - codex_ticket_service.go：票据模型、存储/注入/门控、生命周期；
//   - codex_ticket_harvest.go：主动打票探测、代理池、节奏控制；
//   - codex_ticket_observation.go：真实流量观测（捕获新票 + 失效去重）；
//   - codex_ticket_settings.go：SettingService 扩展（开关/代理池/节奏）；
//   - codex_ticket_history.go：流水/事件/失效查询；
//   - codex_ticket_challenge.go：内置最小可用挑战生成器（不依赖 P4 指纹库）。
// 仓储由 repository 包以窄接口实现（新增文件），Lead 在 main.go 接线。

// CodexTicketAttempt 是一条打票流水。刻意只包含元数据：票据本体、OAuth 凭证、
// 代理 URL 永不进入该结构（它会被管理端读取并可能被导出）。
type CodexTicketAttempt struct {
	ID                 int64      `json:"id"`
	AccountID          int64      `json:"account_id"`
	Model              string     `json:"model"`
	OccurredAt         time.Time  `json:"occurred_at"`
	Outcome            string     `json:"outcome"`
	Trigger            string     `json:"trigger"`
	HTTPStatus         *int       `json:"http_status,omitempty"`
	TicketLength       *int       `json:"ticket_length,omitempty"`
	DurationMS         int        `json:"duration_ms"`
	ReasonCode         string     `json:"reason_code,omitempty"`
	ProxyID            *int64     `json:"proxy_id,omitempty"`
	ProxyName          string     `json:"proxy_name,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	TicketGenerationID *string    `json:"ticket_generation_id,omitempty"`
	VerificationMethod string     `json:"verification_method,omitempty"`
	TurnStatePresent   *bool      `json:"turn_state_present,omitempty"`
	CookiePresent      *bool      `json:"cookie_present,omitempty"`
	// ChallengeExpectedCount / ParsedNumberCount 是探测体挑战的元数据：
	// 预期整数个数与模型输出中实际解析到的 1..355 整数个数。当前实现不做指纹
	// 验真（P4 落地后可在此扩展），仅用于管理端诊断打票质量。
	ChallengeExpectedCount *int `json:"challenge_expected_count,omitempty"`
	ParsedNumberCount      *int `json:"parsed_number_count,omitempty"`
}

// CodexTicketAttemptRepository 持久化打票流水，并提供跨副本的打票互斥锁。
type CodexTicketAttemptRepository interface {
	Insert(context.Context, *CodexTicketAttempt) error
	List(context.Context, int64, string, bool, int, int) ([]CodexTicketAttempt, int64, error)
	Cleanup(context.Context) error
	// TryLock 返回 (解锁函数, 是否拿到锁, error)。拿不到锁时解锁函数为 nil。
	TryLock(context.Context, int64, string) (func(), bool, error)
}

// CodexTicketEvent 是打票流水与失效流水的统一事件视图（管理端时间线）。
type CodexTicketEvent map[string]any

// CodexTicketEventRepository 按时间线合并查询 attempt 与 invalidation。
type CodexTicketEventRepository interface {
	ListEvents(context.Context, int64, string, string, time.Time, time.Time, int, int) ([]CodexTicketEvent, int64, error)
}

// CodexTicketRecentEvent 是账号列表页需要的「最近一次打票/失效」摘要。
type CodexTicketRecentEvent struct {
	Model      string    `json:"model"`
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
}

// CodexTicketRecentRepository 批量查询多个账号的最近事件。
type CodexTicketRecentRepository interface {
	LatestEvents(context.Context, []int64) (map[int64]CodexTicketRecentEvent, error)
}

// CodexTicketInvalidationCredentialsChanged 是当前唯一的失效原因：
// 注入的 turn-state 被上游换掉，且 __oailb 路由 Cookie 同时变化——真实 Codex
// 客户端在同一回合内不会产生这种形态，说明这一代票据已被上游拒绝/轮换。
const CodexTicketInvalidationCredentialsChanged = "upstream_turn_state_and_oailb_changed"

// CodexTicketInvalidation 是一条票据失效流水（详情）。
type CodexTicketInvalidation struct {
	ID                 int64     `json:"id"`
	AccountID          int64     `json:"account_id"`
	Model              string    `json:"model"`
	TicketGenerationID string    `json:"ticket_generation_id"`
	OccurredAt         time.Time `json:"occurred_at"`
	RecordedAt         time.Time `json:"recorded_at"`
	ReasonCode         string    `json:"reason_code"`
	ResponseHTTPStatus *int      `json:"response_http_status"`
	RequestKind        string    `json:"request_kind"`
	RequestRoute       string    `json:"request_route"`
	InvalidatedCurrent bool      `json:"invalidated_current"`
	OriginalTicket     *string   `json:"original_ticket,omitempty"`
	OriginalCookie     *string   `json:"original_cookie,omitempty"`
	ReturnedTicket     string    `json:"returned_ticket,omitempty"`
	ReturnedCookie     *string   `json:"returned_cookie,omitempty"`
	ReturnedSetCookies []string  `json:"returned_set_cookies,omitempty"`
}

// CodexTicketInvalidationSummary 是失效流水列表项：只给存在性布尔，不回传票据。
type CodexTicketInvalidationSummary struct {
	ID                     int64     `json:"id"`
	AccountID              int64     `json:"account_id"`
	Model                  string    `json:"model"`
	TicketGenerationID     string    `json:"ticket_generation_id"`
	OccurredAt             time.Time `json:"occurred_at"`
	RecordedAt             time.Time `json:"recorded_at"`
	ReasonCode             string    `json:"reason_code"`
	ResponseHTTPStatus     *int      `json:"response_http_status"`
	RequestKind            string    `json:"request_kind"`
	RequestRoute           string    `json:"request_route"`
	InvalidatedCurrent     bool      `json:"invalidated_current"`
	OriginalTicketPresent  bool      `json:"original_ticket_present"`
	OriginalCookiePresent  bool      `json:"original_cookie_present"`
	ReturnedCookiePresent  bool      `json:"returned_cookie_present"`
	ReturnedSetCookieCount int       `json:"returned_set_cookie_count"`
}

// Summary 把详情降级为列表项。
func (event CodexTicketInvalidation) Summary() CodexTicketInvalidationSummary {
	return CodexTicketInvalidationSummary{
		ID: event.ID, AccountID: event.AccountID, Model: event.Model,
		TicketGenerationID: event.TicketGenerationID, OccurredAt: event.OccurredAt,
		RecordedAt: event.RecordedAt, ReasonCode: event.ReasonCode,
		ResponseHTTPStatus: event.ResponseHTTPStatus, RequestKind: event.RequestKind,
		RequestRoute: event.RequestRoute, InvalidatedCurrent: event.InvalidatedCurrent,
		OriginalTicketPresent:  event.OriginalTicket != nil,
		OriginalCookiePresent:  event.OriginalCookie != nil,
		ReturnedCookiePresent:  event.ReturnedCookie != nil,
		ReturnedSetCookieCount: len(event.ReturnedSetCookies),
	}
}

// CodexTicketLifecycleRepository 维护「当前票据」与「失效流水」的一致性：
// Invalidate 必须在同一事务里完成「插入失效事件（generation 去重）」与
// 「仅当账号 extra 中仍是这一代票据时才删除」两个动作。
type CodexTicketLifecycleRepository interface {
	CurrentTicket(context.Context, int64, string) (json.RawMessage, error)
	Invalidate(context.Context, *CodexTicketInvalidation) (bool, error)
	ListInvalidations(context.Context, int64, string, time.Time, time.Time, int, int) ([]CodexTicketInvalidationSummary, int64, error)
	GetInvalidation(context.Context, int64, int64) (*CodexTicketInvalidation, error)
}

// CodexTicketTokenProvider 提供 OpenAI OAuth 的可用 access token。
// OpenAIGatewayService.GetAccessToken 恰好实现该窄接口，Lead 可直接传入网关服务；
// 未配置时服务降级为直接读取账号凭据（不做刷新）。
type CodexTicketTokenProvider interface {
	GetAccessToken(ctx context.Context, account *Account) (string, string, error)
}

// CodexTicketExtraStore 是账号 extra 中票据键的窄读写接口。
// repository 包在新增文件里为 accountRepository 实现；未实现时服务降级为
// AccountRepository.UpdateExtra（语义一致，但会额外触发调度 outbox，见 README 说明）。
type CodexTicketExtraStore interface {
	UpdateCodexTicketExtra(ctx context.Context, accountID int64, key string, value any) error
	RemoveCodexTicketExtra(ctx context.Context, accountID int64, key string) error
}

// CodexTicketHarvestResult 是手动/自动打票单次尝试的结果。
type CodexTicketHarvestResult struct {
	CodexTicketAttempt
	TicketStatus    CodexTicketStatus `json:"ticket_status"`
	HistoryRecorded bool              `json:"history_recorded"`
}

// CodexTicketStatus 是管理端看到的票据摘要：绝不含 state blob 本体。
type CodexTicketStatus struct {
	Model                string     `json:"model"`
	Length               int        `json:"length"`
	CapturedAt           *time.Time `json:"captured_at,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	TurnStatePresent     bool       `json:"turn_state_present"`
	CookiePresent        bool       `json:"cookie_present"`
	Ready                bool       `json:"ready"`
	Blocked              bool       `json:"blocked"`
	ReusingExpired       bool       `json:"reusing_expired,omitempty"`
	RemainingSeconds     int64      `json:"remaining_seconds"`
	ExpectedTicketLength int        `json:"expected_ticket_length"`
	HarvestEnabled       bool       `json:"harvest_enabled"`
	HarvestPaused        bool       `json:"harvest_paused"`
	ParticipationEnabled bool       `json:"participation_enabled"`
	QuotaResetAt         *time.Time `json:"quota_reset_at,omitempty"`
	HarvestResumeAt      *time.Time `json:"harvest_resume_at,omitempty"`
	Reason               string     `json:"reason,omitempty"`
}

// CodexTicketProxyPool 是打票代理池设置（复用本地「IP 设置」里的代理记录）。
// Mode=all 表示全部未过期代理参与轮换；Mode=custom 只轮换 ProxyIDs 中的代理。
type CodexTicketProxyPool struct {
	Mode     string  `json:"mode"`
	ProxyIDs []int64 `json:"proxy_ids"`
}

// CodexTicketCadence 是打票节奏：失败退避区间与成功票据的主动刷新周期。
type CodexTicketCadence struct {
	RetryMinSeconds int `json:"retry_min_seconds"`
	RetryMaxSeconds int `json:"retry_max_seconds"`
	RefreshSeconds  int `json:"refresh_seconds"`
}

// CodexTicketUsageError 用于把「服务不可用/参数不合法」映射成管理端可读错误。
var (
	ErrCodexTicketUnavailable = codexTicketError("codex ticket harvesting unavailable")
	ErrCodexTicketBusy        = codexTicketError("codex ticket attempt already running")
	ErrCodexTicketNoProxy     = codexTicketError("no available codex ticket proxy")
	ErrCodexTicketModel       = codexTicketError("unsupported codex ticket model")
	ErrCodexTicketRateLimited = codexTicketError("account or model is rate limited")
)

// codexTicketError 是本文件内的轻量错误构造，避免为票据功能再引入新的错误包依赖。
type codexTicketError string

func (e codexTicketError) Error() string { return string(e) }
