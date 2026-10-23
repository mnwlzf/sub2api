package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAI BPS（ChatGPT Excel/Basis Points 权益）平台的静态契约。
//
// 该平台与 OpenAI 平台完全隔离：固定上游、固定鉴权头、仅 Responses HTTP 协议。
// 本文件只放"平台是什么"的纯函数与校验，网关/凭证/状态等运行期实现分别在
// openai_bps_gateway.go / openai_bps_credentials.go / openai_bps_state.go。
const (
	// OpenAIBPSUpstreamURL 是 BPS 固定上游端点。
	// 不接受账号级 base_url 或客户端参数覆盖：该平台只有一个上游，开放覆盖
	// 只会扩大 SSRF 与凭证外泄面。
	OpenAIBPSUpstreamURL = "https://bps.openai.com/basispoints/api/responses"
	// OpenAIBPSAuthModeHeader / OpenAIBPSAuthModeChatGPT 是上游要求的鉴权模式头。
	OpenAIBPSAuthModeHeader  = "x-basispoints-auth-mode"
	OpenAIBPSAuthModeChatGPT = "chatgpt"
)

// OpenAIBPSPlatformConfig 返回 BPS 平台配置（含 kill switch）。
func OpenAIBPSPlatformConfig() config.OpenAIBPSConfig {
	return config.LoadOpenAIBPSConfig()
}

// OpenAIBPSEnabled 报告 BPS 平台级开关是否打开（默认关闭）。
// 关闭时 BPS 账号不参与调度、入口返回明确错误，既有数据保留。
func OpenAIBPSEnabled() bool {
	return OpenAIBPSPlatformConfig().Enabled
}

// ValidateOpenAIBPSAccountInput 校验 BPS 账号的创建/更新输入。
//
// incoming 是本次提交的 credentials（可能被前端脱敏为空对象）；existing 是库里
// 已有的 credentials（创建时为 nil）。access_token 是敏感键，更新时空值/缺省
// 表示"保留原值"，因此只有 incoming 与 existing 都没有 token 时才报错。
func ValidateOpenAIBPSAccountInput(platform, accountType string, incoming, existing map[string]any) error {
	if platform != PlatformOpenAIBPS {
		return nil
	}
	if strings.TrimSpace(accountType) != AccountTypeOAuth {
		return infraerrors.Newf(http.StatusBadRequest, "INVALID_OPENAI_BPS_ACCOUNT_TYPE",
			"OpenAI BPS 账号仅支持 oauth 类型，当前为 %q", accountType)
	}
	token := strings.TrimSpace(stringValue(incoming["access_token"]))
	if token == "" {
		token = strings.TrimSpace(stringValue(existing["access_token"]))
	}
	if token == "" {
		return infraerrors.Newf(http.StatusBadRequest, "OPENAI_BPS_ACCESS_TOKEN_REQUIRED",
			"OpenAI BPS 账号必须填写 Access Token")
	}
	if accountID, ok := incoming["chatgpt_account_id"]; ok {
		value, isString := accountID.(string)
		if !isString {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_OPENAI_BPS_ACCOUNT_ID",
				"chatgpt_account_id 必须是字符串")
		}
		if strings.ContainsAny(value, " \t\r\n") {
			return infraerrors.Newf(http.StatusBadRequest, "INVALID_OPENAI_BPS_ACCOUNT_ID",
				"chatgpt_account_id 不能包含空白字符")
		}
	}
	return nil
}

// OpenAIBPSDefaultModels 返回 BPS 分组的默认候选模型（配置可覆盖）。
func OpenAIBPSDefaultModels() []string {
	models := OpenAIBPSPlatformConfig().DefaultModels
	out := make([]string, len(models))
	copy(out, models)
	return out
}

// IsOpenAIBPSPlatform 报告平台值是否为 BPS。
func IsOpenAIBPSPlatform(platform string) bool {
	return platform == PlatformOpenAIBPS
}

// NormalizeOpenAIBPSAccountCredentials 归一化 BPS 账号提交的凭证：
// 去空白、剥离误粘贴的 "Bearer " 前缀；把前端回传的脱敏占位符或空串视为
// "未提供"（删除该键），交由更新路径的敏感键合并语义保留库中原 token。
func NormalizeOpenAIBPSAccountCredentials(credentials map[string]any) map[string]any {
	if credentials == nil {
		return nil
	}
	raw, ok := credentials["access_token"]
	if !ok {
		return credentials
	}
	token, _ := raw.(string)
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" || token == OpenAIBPSRedactedAccessTokenPlaceholder {
		delete(credentials, "access_token")
		return credentials
	}
	credentials["access_token"] = token
	return credentials
}
