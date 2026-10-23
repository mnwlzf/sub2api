package service

import (
	_ "embed"
	"net/http"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// DefaultOpenAIRequestTimezone 是 OpenAI 账号未显式配置请求时区时使用的默认时区。
const DefaultOpenAIRequestTimezone = "Asia/Singapore"

// openAIRequestTimezoneExtraKey 是 accounts.extra 中保存账号级请求时区的键名。
const openAIRequestTimezoneExtraKey = "openai_request_timezone"

//go:embed openai_request_timezones.txt
var openAIRequestTimezoneNames string

var openAIRequestTimezoneOptions = strings.Fields(openAIRequestTimezoneNames)

var openAIRequestTimezoneAllowed = func() map[string]struct{} {
	allowed := make(map[string]struct{}, len(openAIRequestTimezoneOptions))
	for _, name := range openAIRequestTimezoneOptions {
		allowed[name] = struct{}{}
	}
	return allowed
}()

// OpenAIRequestTimezoneOptions 返回可选时区列表（按名称排序的副本）。
// 列表不含中国大陆与港澳台时区；默认时区始终包含在列表内。
func OpenAIRequestTimezoneOptions() []string {
	options := append([]string(nil), openAIRequestTimezoneOptions...)
	sort.Strings(options)
	return options
}

// ValidateOpenAIRequestTimezoneExtra 校验账号 extra 中的 openai_request_timezone。
// 未设置或显式设为空串表示使用默认时区；非 OpenAI 平台不允许配置非空时区。
func ValidateOpenAIRequestTimezoneExtra(platform string, extra map[string]any) error {
	if extra == nil {
		return nil
	}
	raw, exists := extra[openAIRequestTimezoneExtraKey]
	if !exists || raw == nil {
		return nil
	}
	name, ok := raw.(string)
	if !ok || (name != "" && !isAllowedOpenAIRequestTimezone(name)) || (name != "" && platform != PlatformOpenAI) {
		return infraerrors.New(http.StatusBadRequest, "INVALID_OPENAI_REQUEST_TIMEZONE", "openai_request_timezone must be an allowed IANA timezone for an OpenAI account")
	}
	return nil
}

func isAllowedOpenAIRequestTimezone(name string) bool {
	if _, ok := openAIRequestTimezoneAllowed[name]; !ok {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// GetOpenAIRequestTimezone 返回账号级请求时区。
// 非 OpenAI 账号、未配置、配置为空串或配置了非法时区时回落到默认时区。
func (a *Account) GetOpenAIRequestTimezone() string {
	if a != nil && a.IsOpenAI() {
		if name := a.GetExtraString(openAIRequestTimezoneExtraKey); isAllowedOpenAIRequestTimezone(name) {
			return name
		}
	}
	return DefaultOpenAIRequestTimezone
}
