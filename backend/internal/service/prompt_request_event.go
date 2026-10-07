package service

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

// 请求级提示词策略记录。
//
// 与 prompt_admin_events（管理操作审计）分开：这里记录的是“运行期实际发生了什么”，
// 用于回答“这个请求注入了没有、为什么没注入、重试时有没有换版本”。
//
// 写入必须尽力而为：记录失败只打日志/指标，绝不重试已经成功的上游请求，也不阻断请求。

// PromptRequestEvent 是一条运行期记录（不含提示词正文与客户请求体）。
type PromptRequestEvent struct {
	ID              int64     `json:"id"`
	RequestID       string    `json:"request_id,omitempty"`
	AttemptNo       int       `json:"attempt_no"`
	GroupID         *int64    `json:"group_id,omitempty"`
	AccountID       *int64    `json:"account_id,omitempty"`
	ClientModel     string    `json:"client_model,omitempty"`
	UpstreamModel   string    `json:"upstream_model,omitempty"`
	OutboundProfile string    `json:"outbound_profile,omitempty"`
	BindingSource   string    `json:"binding_source,omitempty"`
	VersionID       *int64    `json:"version_id,omitempty"`
	ManifestSHA256  string    `json:"manifest_sha256,omitempty"`
	Applied         bool      `json:"applied"`
	Reason          string    `json:"reason"`
	AddedBytes      int       `json:"added_bytes"`
	ApplyDurationMs int       `json:"apply_duration_ms"`
	CreatedAt       time.Time `json:"created_at"`
}

// PromptRequestEventRecorder 是运行期记录的写入端口。
//
// 实现必须是非阻塞或低成本的：它在出站热路径上被调用。
type PromptRequestEventRecorder interface {
	RecordPromptRequestEvent(ctx context.Context, event PromptRequestEvent)
}

// PromptRequestEventFilter 用于后台查询。
type PromptRequestEventFilter struct {
	RequestID *string
	GroupID   *int64
	VersionID *int64
	Applied   *bool
	Limit     int
}

// SetPromptEventRecorder 注入运行期记录器。
//
// 使用 setter 而不是构造函数参数：OpenAIGatewayService 的构造签名已经很长，
// 且该依赖是可选的（未注入时记录被静默跳过，不影响注入行为）。
func (s *OpenAIGatewayService) SetPromptEventRecorder(recorder PromptRequestEventRecorder) {
	if s == nil {
		return
	}
	s.promptEventRecorder = recorder
}

// promptRequestIDFrom 读取网关已有的请求 ID。
func promptRequestIDFrom(c interface{ GetString(string) string }) string {
	if c == nil {
		return ""
	}
	return c.GetString("request_id")
}

const promptAttemptGinKey = "sub2api.prompt_attempt_no"

// nextPromptAttemptNo 返回本次逻辑请求内的尝试序号（从 1 开始）。
//
// ApplyFrozenPromptInjection 每次上游尝试调用一次，因此这里计数与真实尝试次数一致，
// 便于后台看出“同一个 request_id 下重试了几次、每次是否仍用同一版本”。
func nextPromptAttemptNo(c *gin.Context) int {
	if c == nil {
		return 1
	}
	current := 1
	if value, ok := c.Get(promptAttemptGinKey); ok {
		if n, ok := value.(int); ok && n > 0 {
			current = n + 1
		}
	}
	c.Set(promptAttemptGinKey, current)
	return current
}

// recordPromptRequestEvent 尽力而为地写入一条运行期记录。
//
// 失败只记录日志，不返回错误：上游请求可能已经成功，不能因为审计写入失败而重试。
func (s *OpenAIGatewayService) recordPromptRequestEvent(
	c *gin.Context,
	policy *FrozenPromptPolicy,
	profile string,
	upstreamModel string,
	attemptNo int,
	applied bool,
	reason string,
	addedBytes int,
	elapsed time.Duration,
) {
	if s == nil || s.promptEventRecorder == nil {
		return
	}
	event := PromptRequestEvent{
		RequestID:       promptRequestIDFrom(c),
		AttemptNo:       attemptNo,
		UpstreamModel:   upstreamModel,
		OutboundProfile: profile,
		Applied:         applied,
		Reason:          reason,
		AddedBytes:      addedBytes,
		ApplyDurationMs: int(elapsed.Milliseconds()),
	}
	if policy != nil {
		event.BindingSource = policy.BindingSource
		event.ManifestSHA256 = policy.ManifestSHA256
		if policy.VersionID > 0 {
			versionID := policy.VersionID
			event.VersionID = &versionID
		}
		if policy.GroupID > 0 {
			groupID := policy.GroupID
			event.GroupID = &groupID
		}
		if policy.AccountID > 0 {
			accountID := policy.AccountID
			event.AccountID = &accountID
		}
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	s.promptEventRecorder.RecordPromptRequestEvent(ctx, event)
}
