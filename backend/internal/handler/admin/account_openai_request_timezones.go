package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// GetOpenAIRequestTimezones 返回 OpenAI 账号可选的请求时区列表。
// 列表不含中国大陆与港澳台时区；前端据此渲染「请求时区」下拉。
func (h *AccountHandler) GetOpenAIRequestTimezones(c *gin.Context) {
	response.Success(c, gin.H{
		"default":   service.DefaultOpenAIRequestTimezone,
		"timezones": service.OpenAIRequestTimezoneOptions(),
	})
}
