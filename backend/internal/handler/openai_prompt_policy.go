package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// 文本提示词策略的请求级冻结。
//
// 冻结时机：首个账号被选中之后、首次网络发送之前。这样既知道本次请求实际走哪个
// 分组和账号（账号覆盖依赖账号身份），又保证在真正发出请求前策略已经确定。
//
// 为什么不在循环里每次都解析：一次逻辑请求可能因为限流/5xx 换多个账号。如果每次
// 都重新解析，第一次可能用版本 A、重试到另一个账号时静默换成版本 B（甚至关闭），
// 表现为“同一请求两次回答口径不同”。这里每个逻辑请求只解析一次，后续尝试复用
// 同一份冻结策略，与设计文档第 6.2 节一致。

// freezeTextPromptPolicy 解析并冻结本请求的提示词策略。
//
// 返回 false 表示已经写出错误响应，调用方必须立即 return。
// 功能未启用、解析器未注入、或已经冻结过时直接返回 true（不产生额外开销）。
func (h *OpenAIGatewayHandler) freezeTextPromptPolicy(c *gin.Context, groupID *int64, accountID int64, clientModel string) bool {
	if h == nil || h.promptPolicyResolver == nil || !h.promptPolicyResolver.Enabled() {
		return true
	}
	if service.FrozenPromptPolicyFromGin(c) != nil {
		// 已经冻结：重试/换号必须沿用同一策略。
		return true
	}
	var resolvedGroupID int64
	if groupID != nil {
		resolvedGroupID = *groupID
	}
	policy, err := h.promptPolicyResolver.Resolve(c.Request.Context(), resolvedGroupID, accountID, clientModel)
	if err != nil {
		// 已启用但无法安全解析策略：宁可拒绝请求，也不能在配置不可信的情况下
		// 按“没有提示词”发送 —— 否则后台显示启用、实际行为却与配置不一致。
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type":    "service_unavailable",
			"code":    "prompt_config_unavailable",
			"message": err.Error(),
		}})
		return false
	}
	service.WithFrozenPromptPolicy(c, policy)
	return true
}
