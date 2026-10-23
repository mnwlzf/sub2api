package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// BPS 计费接入层。
//
// BPS 只做三件事，其余全部复用既有 OpenAI 计费链路：
//  1. 把上游 Responses usage 归一成 OpenAIUsage 口径；
//  2. 用纯函数构造 OpenAIRecordUsageInput（分组/渠道价格、倍率、用量日志、
//     用户平台配额都由 OpenAIGatewayService.RecordUsage 内部处理）；
//  3. 在 handler 终态（JSON/SSE、成功/失败）把已取得的 usage 交给窄接口记录。
//
// 计费资格检查与在途预留属于 handler 包的私有链路（billingErrorDetails /
// reserveInflightBalance），这里只暴露窄接口由 Lead 适配，绝不复制那份逻辑。
const (
	// OpenAIBPSInboundEndpointResponses / Compact 是 BPS 的客户端入口路径。
	OpenAIBPSInboundEndpointResponses = "/v1/responses"
	OpenAIBPSInboundEndpointCompact   = "/v1/responses/compact"
	// OpenAIBPSUpstreamEndpointPath 是固定上游的路径（用量日志上游端点列）。
	OpenAIBPSUpstreamEndpointPath = "/basispoints/api/responses"

	// openAIBPSSubscriptionContextKey 是鉴权中间件写入订阅的 gin 上下文键
	//（middleware.ContextKeySubscription 的字符串值）。service 不能反向 import
	// middleware（循环依赖），因此这里按同值读取，与 getAPIKeyFromContext 的
	// "api_key" 键同一先例。
	openAIBPSSubscriptionContextKey = "subscription"

	// openAIBPSUsageErrorBodyPeekBytes 是上游错误体解析上限：BPS 错误信封很小，
	// 1 MiB 足以覆盖 usage 与错误码，同时不会为异常响应占用过多内存。
	openAIBPSUsageErrorBodyPeekBytes = 1 << 20
)

// OpenAIBPSUsage 是 BPS 上游 Responses usage 的归一化结果。
//
// OpenAI 口径：InputTokens 是总输入（含缓存读写），RecordUsage 会自行拆成互斥
// 桶；这里只保留原始字段，不在解析层做减法。
type OpenAIBPSUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	TotalTokens              int `json:"total_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	ReasoningOutputTokens    int `json:"reasoning_output_tokens,omitempty"`
}

// IsZero 报告 usage 是否完全没有 token。零 usage 仍算"上游给出了 usage"，
// 是否记录由调用方决定（失败但可取得 usage 的场景要记录）。
func (u OpenAIBPSUsage) IsZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 &&
		u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 &&
		u.ReasoningOutputTokens == 0
}

// OpenAIBPSUsageFromJSON 从上游响应/错误体里解析 usage。
//
// 返回 ok=false 表示没有可用的 usage 对象（字段缺失、null 或不是对象），
// 调用方不得据此写出 0 token 的用量行。兼容 Responses 的
// input_tokens/output_tokens/input_tokens_details.cached_tokens 以及部分上游
// 额外返回的 cache_creation 字段。
func OpenAIBPSUsageFromJSON(data []byte) (OpenAIBPSUsage, bool) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return OpenAIBPSUsage{}, false
	}
	usage := gjson.GetBytes(data, "usage")
	if !usage.IsObject() {
		// 兼容 {"response":{"usage":...}} 形状的错误体/包装响应。
		usage = gjson.GetBytes(data, "response.usage")
	}
	if !usage.Exists() || !usage.IsObject() {
		return OpenAIBPSUsage{}, false
	}
	return openAIBPSUsageFromResult(usage), true
}

// OpenAIBPSUsageFromValue 解析 gin 上下文里保存的 usage（JSON 解码后的 any）。
// 该值本身就是 usage 对象（协议层写入的是 response["usage"]），因此包装成
// {"usage": ...} 后复用同一套字段解析。
func OpenAIBPSUsageFromValue(value any) (OpenAIBPSUsage, bool) {
	if value == nil {
		return OpenAIBPSUsage{}, false
	}
	if usage, ok := value.(OpenAIBPSUsage); ok {
		return usage, true
	}
	encoded, err := json.Marshal(map[string]any{"usage": value})
	if err != nil {
		return OpenAIBPSUsage{}, false
	}
	return OpenAIBPSUsageFromJSON(encoded)
}

// OpenAIBPSUsageFromContext 读取协议层写入的 usage（OpenAIBPSUsageContextKey）。
// 成功、response.failed、结构化校验失败与断流前已到达的 usage 都会保留。
func OpenAIBPSUsageFromContext(c *gin.Context) (OpenAIBPSUsage, bool) {
	if c == nil {
		return OpenAIBPSUsage{}, false
	}
	value, exists := c.Get(OpenAIBPSUsageContextKey)
	if !exists {
		return OpenAIBPSUsage{}, false
	}
	return OpenAIBPSUsageFromValue(value)
}

// openAIBPSUsageFromResult 从 usage JSON 对象取字段。
func openAIBPSUsageFromResult(usage gjson.Result) OpenAIBPSUsage {
	parsed := OpenAIBPSUsage{
		InputTokens:              int(usage.Get("input_tokens").Int()),
		OutputTokens:             int(usage.Get("output_tokens").Int()),
		TotalTokens:              int(usage.Get("total_tokens").Int()),
		CacheReadInputTokens:     int(usage.Get("input_tokens_details.cached_tokens").Int()),
		CacheCreationInputTokens: int(usage.Get("input_tokens_details.cache_creation_tokens").Int()),
		ReasoningOutputTokens:    int(usage.Get("output_tokens_details.reasoning_tokens").Int()),
	}
	if parsed.InputTokens == 0 {
		// 兼容个别上游回退到 chat 口径的字段名；BPS 正常不会走到这里。
		parsed.InputTokens = int(usage.Get("prompt_tokens").Int())
	}
	if parsed.OutputTokens == 0 {
		parsed.OutputTokens = int(usage.Get("completion_tokens").Int())
	}
	if parsed.CacheReadInputTokens == 0 {
		parsed.CacheReadInputTokens = int(usage.Get("cache_read_input_tokens").Int())
	}
	if parsed.CacheCreationInputTokens == 0 {
		parsed.CacheCreationInputTokens = int(usage.Get("cache_creation_input_tokens").Int())
	}
	// 负数只可能来自畸形上游：按 0 处理，避免把负 token 写进计费。
	parsed.InputTokens = max(parsed.InputTokens, 0)
	parsed.OutputTokens = max(parsed.OutputTokens, 0)
	parsed.CacheReadInputTokens = max(parsed.CacheReadInputTokens, 0)
	parsed.CacheCreationInputTokens = max(parsed.CacheCreationInputTokens, 0)
	parsed.ReasoningOutputTokens = max(parsed.ReasoningOutputTokens, 0)
	// total 在钳制之后回算：上游给出的 total 只作为提示，绝不能为负。
	if parsed.TotalTokens <= 0 {
		parsed.TotalTokens = parsed.InputTokens + parsed.OutputTokens
	}
	return parsed
}

// OpenAIBPSUsageRecorder 是用量记录窄接口。
// OpenAIGatewayService.RecordUsage 的方法集直接满足该接口（Lead 接线用）。
type OpenAIBPSUsageRecorder interface {
	RecordUsage(ctx context.Context, input *OpenAIRecordUsageInput) error
}

var _ OpenAIBPSUsageRecorder = (*OpenAIGatewayService)(nil)

// OpenAIBPSBillingGate 是选号前的计费资格检查窄接口。
//
// 实现方（Lead 在 handler/wire 层适配）负责调用
// BillingCacheService.CheckBillingEligibility，并把计费错误映射成
// *OpenAIBPSBillingFailure（可直接复用 billingErrorDetails 的
// status/code/message/retryAfter）或 *infraerrors.ApplicationError。BPS 侧不
// 复制余额/订阅/平台配额的判定逻辑。
type OpenAIBPSBillingGate interface {
	CheckOpenAIBPSBillingEligibility(ctx context.Context, apiKey *APIKey, subscription *UserSubscription) error
}

// OpenAIBPSBillingFailure 是计费闸门的结构化失败。
//
// 字段与 handler 包的 billingErrorDetails 一一对应，Lead 只需把该函数的四个
// 返回值填进来即可（RetryAfterSeconds 由 BPS handler 写成 Retry-After 头）。
type OpenAIBPSBillingFailure struct {
	Status            int
	Code              string
	Message           string
	RetryAfterSeconds int
}

// Error 实现 error。
func (f *OpenAIBPSBillingFailure) Error() string {
	if f == nil {
		return "bps billing failure"
	}
	if message := strings.TrimSpace(f.Message); message != "" {
		return message
	}
	return http.StatusText(f.Status)
}

// OpenAIBPSBillingCacheGate 是 OpenAIBPSBillingGate 的默认实现：
// 直接复用 BillingCacheService.CheckBillingEligibility（余额/订阅/平台配额）。
//
// MapError 由 Lead 注入以复用 handler.billingErrorDetails 的映射；未注入时
// 计费错误按 503 bps_billing_unavailable 返回（绝不放行未付费请求）。
type OpenAIBPSBillingCacheGate struct {
	Billing  *BillingCacheService
	MapError func(error) *OpenAIBPSBillingFailure
}

var _ OpenAIBPSBillingGate = (*OpenAIBPSBillingCacheGate)(nil)

// CheckOpenAIBPSBillingEligibility 执行一次计费资格检查。
func (g *OpenAIBPSBillingCacheGate) CheckOpenAIBPSBillingEligibility(ctx context.Context, apiKey *APIKey, subscription *UserSubscription) error {
	if g == nil || g.Billing == nil || apiKey == nil || apiKey.User == nil {
		return nil
	}
	err := g.Billing.CheckBillingEligibility(ctx, apiKey.User, apiKey, apiKey.Group, subscription, QuotaPlatform(ctx, apiKey))
	if err == nil {
		return nil
	}
	if g.MapError != nil {
		if failure := g.MapError(err); failure != nil {
			return failure
		}
	}
	return err
}

// OpenAIBPSUsageRecord 是一次 BPS 请求记录用量所需的全部输入。
type OpenAIBPSUsageRecord struct {
	APIKey       *APIKey
	User         *User
	Account      *Account
	Subscription *UserSubscription
	// Model 是客户端请求的模型（计费基准），UpstreamModel 是映射后实际发送的模型。
	Model         string
	UpstreamModel string
	// RequestID / ResponseID 分别对应上游 x-request-id 与 response.id。
	RequestID  string
	ResponseID string
	Usage      OpenAIBPSUsage
	Stream     bool
	Duration   time.Duration
	// UpstreamHeaders 让 RecordUsage 复用既有上游请求 ID 解析。
	UpstreamHeaders    http.Header
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string
	IPAddress          string
	SessionID          string
	RequestPayloadHash string
	APIKeyService      APIKeyQuotaUpdater
	QuotaPlatform      string
	PricingAt          time.Time
	ChannelUsageFields
}

// BuildOpenAIBPSRecordUsageInput 把 BPS 结果转换成既有 RecordUsage 入参（纯函数）。
//
// 字段口径与 openai_embeddings.go 的成功路径一致：Result.Model 保留客户端
// 模型（计费基准），映射后的上游模型放在 UpstreamModel；缓存 token 原样放入
// 缓存桶，由 RecordUsage 统一做互斥拆分与成本计算。
func BuildOpenAIBPSRecordUsageInput(record OpenAIBPSUsageRecord) *OpenAIRecordUsageInput {
	user := record.User
	if user == nil && record.APIKey != nil {
		user = record.APIKey.User
	}
	upstreamModel := strings.TrimSpace(record.UpstreamModel)
	model := strings.TrimSpace(record.Model)
	result := &OpenAIForwardResult{
		RequestID:       strings.TrimSpace(record.RequestID),
		ResponseID:      strings.TrimSpace(record.ResponseID),
		UpstreamHeaders: record.UpstreamHeaders,
		Model:           model,
		Stream:          record.Stream,
		Duration:        record.Duration,
		Usage: OpenAIUsage{
			InputTokens:              record.Usage.InputTokens,
			OutputTokens:             record.Usage.OutputTokens,
			CacheReadInputTokens:     record.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: record.Usage.CacheCreationInputTokens,
		},
	}
	if result.RequestID == "" {
		result.RequestID = result.ResponseID
	}
	// 只有真正发生映射时才记录 UpstreamModel：与既有 OpenAI 路径
	//（未映射时 UpstreamModel 为空）保持一致，避免用量日志出现重复模型列。
	if upstreamModel != "" && !strings.EqualFold(upstreamModel, model) {
		result.UpstreamModel = upstreamModel
	}
	channelFields := record.ChannelUsageFields
	if strings.TrimSpace(channelFields.OriginalModel) == "" {
		channelFields.OriginalModel = model
	}
	if strings.TrimSpace(channelFields.ChannelMappedModel) == "" {
		channelFields.ChannelMappedModel = upstreamModel
	}
	return &OpenAIRecordUsageInput{
		Result:             result,
		APIKey:             record.APIKey,
		User:               user,
		Account:            record.Account,
		Subscription:       record.Subscription,
		InboundEndpoint:    strings.TrimSpace(record.InboundEndpoint),
		UpstreamEndpoint:   strings.TrimSpace(record.UpstreamEndpoint),
		UserAgent:          strings.TrimSpace(record.UserAgent),
		IPAddress:          strings.TrimSpace(record.IPAddress),
		SessionID:          strings.TrimSpace(record.SessionID),
		RequestPayloadHash: strings.TrimSpace(record.RequestPayloadHash),
		APIKeyService:      record.APIKeyService,
		QuotaPlatform:      strings.TrimSpace(record.QuotaPlatform),
		PricingAt:          record.PricingAt,
		ChannelUsageFields: channelFields,
	}
}

// openAIBPSSubscription 读取鉴权中间件写入的订阅（可能不存在）。
func openAIBPSSubscription(c *gin.Context) *UserSubscription {
	if c == nil {
		return nil
	}
	value, exists := c.Get(openAIBPSSubscriptionContextKey)
	if !exists {
		return nil
	}
	subscription, _ := value.(*UserSubscription)
	return subscription
}

// openAIBPSBillingError 把计费闸门错误收敛成对外错误。
// 结构化失败优先（可携带 Retry-After）；ApplicationError 原样透传；其它错误按
// 503 bps_billing_unavailable 处理，绝不吞成 200。
func openAIBPSBillingError(err error) (error, int) {
	if err == nil {
		return nil, 0
	}
	var failure *OpenAIBPSBillingFailure
	if errors.As(err, &failure) && failure != nil {
		status := failure.Status
		if status <= 0 {
			status = http.StatusServiceUnavailable
		}
		code := strings.TrimSpace(failure.Code)
		if code == "" {
			code = "bps_billing_unavailable"
		}
		message := strings.TrimSpace(failure.Message)
		if message == "" {
			message = http.StatusText(status)
		}
		return infraerrors.Newf(status, code, "%s", message), failure.RetryAfterSeconds
	}
	var appErr *infraerrors.ApplicationError
	if errors.As(err, &appErr) {
		return err, 0
	}
	message := strings.TrimSpace(infraerrors.Message(err))
	if message == "" {
		message = "BPS billing check failed"
	}
	return infraerrors.Newf(http.StatusServiceUnavailable, "bps_billing_unavailable", "%s", message), 0
}

// checkOpenAIBPSBillingEligibility 在选号前执行计费资格检查。
// 未注入闸门时直接放行（Lead 接线前保持既有行为），注入后失败即终止请求。
func (h *OpenAIBPSHandler) checkOpenAIBPSBillingEligibility(c *gin.Context, apiKey *APIKey) bool {
	if h == nil || h.deps.Billing == nil || c == nil || c.Request == nil {
		return true
	}
	if apiKey == nil {
		return true
	}
	if err := h.deps.Billing.CheckOpenAIBPSBillingEligibility(c.Request.Context(), apiKey, openAIBPSSubscription(c)); err != nil {
		logger.FromContext(openAIBPSContext(c)).Info("openai bps: billing eligibility check failed",
			zap.Int64("api_key_id", apiKey.ID), zap.Error(err))
		mapped, retryAfter := openAIBPSBillingError(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		WriteOpenAIBPSError(c, mapped)
		return false
	}
	return true
}

// recordOpenAIBPSUsage 在 handler 终态记录本次请求用量。
//
// 只要协议层拿到了 usage（成功 JSON/SSE、response.failed、结构化校验失败、
// 上游错误体里的 usage），无论最终成功或失败都记录；拿不到 usage 时静默跳过
// （例如网络错误、上游未返回 usage 的 4xx）。
func (h *OpenAIBPSHandler) recordOpenAIBPSUsage(
	c *gin.Context,
	account *Account,
	prepared *openAIBPSPreparedRequest,
	requestID string,
	started time.Time,
	stream bool,
	upstreamHeaders http.Header,
	compact bool,
	body []byte,
	pricingAt time.Time,
) {
	if h == nil || h.deps.Usage == nil || c == nil || account == nil {
		return
	}
	usage, ok := OpenAIBPSUsageFromContext(c)
	if !ok {
		return
	}
	apiKey := getAPIKeyFromContext(c)
	if apiKey == nil || apiKey.User == nil {
		// 没有用户上下文无法计费（例如只注入了 APIKey ID 的测试/内部调用）。
		return
	}
	model := ""
	upstreamModel := ""
	if prepared != nil {
		model = prepared.ClientModel
		upstreamModel = prepared.UpstreamModel
	}
	requestCtx := openAIBPSContext(c)
	record := OpenAIBPSUsageRecord{
		APIKey:             apiKey,
		User:               apiKey.User,
		Account:            account,
		Subscription:       openAIBPSSubscription(c),
		Model:              model,
		UpstreamModel:      upstreamModel,
		RequestID:          requestID,
		ResponseID:         openAIBPSResponseIDFromContext(c),
		Usage:              usage,
		Stream:             stream,
		Duration:           time.Since(started),
		UpstreamHeaders:    upstreamHeaders,
		InboundEndpoint:    openAIBPSInboundEndpoint(compact),
		UpstreamEndpoint:   OpenAIBPSUpstreamEndpointPath,
		UserAgent:          c.GetHeader("User-Agent"),
		IPAddress:          ip.GetClientIP(c),
		SessionID:          ExtractClientSessionID(c),
		RequestPayloadHash: HashUsageRequestPayload(body),
		APIKeyService:      h.deps.APIKeyService,
		QuotaPlatform:      QuotaPlatform(requestCtx, apiKey),
		PricingAt:          pricingAt,
	}
	input := BuildOpenAIBPSRecordUsageInput(record)
	if input == nil || input.Result == nil {
		return
	}
	// 响应已写回客户端：用量记录不能因客户端断开而丢失，剥离取消信号。
	recordCtx := context.WithoutCancel(requestCtx)
	if err := h.deps.Usage.RecordUsage(recordCtx, input); err != nil {
		logger.FromContext(requestCtx).Error("openai bps: record usage failed",
			zap.Int64("account_id", account.ID),
			zap.Int64("api_key_id", apiKey.ID),
			zap.String("request_id", requestID),
			zap.Int("input_tokens", usage.InputTokens),
			zap.Int("output_tokens", usage.OutputTokens),
			zap.Error(err))
	}
}

// openAIBPSResponseIDFromContext 读取协议层写入的上游 response.id。
func openAIBPSResponseIDFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if value, exists := c.Get(OpenAIBPSResponseIDContextKey); exists {
		if responseID, ok := value.(string); ok {
			return strings.TrimSpace(responseID)
		}
	}
	return ""
}

// openAIBPSInboundEndpoint 返回用量日志的入站端点。
func openAIBPSInboundEndpoint(compact bool) string {
	if compact {
		return OpenAIBPSInboundEndpointCompact
	}
	return OpenAIBPSInboundEndpointResponses
}
