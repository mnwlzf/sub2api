package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	httppool "github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/tidwall/gjson"
)

// codexProbeMaxBodyBytes 限制探测响应的读取字节数，够解析 output_text 即可。
const codexProbeMaxBodyBytes = 2 << 20

// CodexDegradationTokenProvider 是取 OpenAI OAuth 访问令牌的窄接口；
// *OpenAITokenProvider 直接满足。setup-token 账号不走刷新链路，直接读凭据。
type CodexDegradationTokenProvider interface {
	GetAccessToken(ctx context.Context, account *Account) (string, error)
}

// CodexDegradationBlockClearer 是解除临时停调的窄接口；*RateLimitService 直接满足。
// 注入后解除封锁时会同时清理 Redis 临时停调缓存与模型级限流。
type CodexDegradationBlockClearer interface {
	ClearTempUnschedulable(ctx context.Context, accountID int64) error
}

// CodexDegradationDeps 是 Codex 降智检测服务的依赖，由 main.go 用现有 wire
// 依赖直接构造：
//
//	deps := service.CodexDegradationDeps{
//	    AccountRepo:  accountRepo,        // wire 已有
//	    Settings:     settingService,     // wire 已有
//	    Tokens:       openAITokenProvider,// wire 已有
//	    RateLimiter:  rateLimitService,   // wire 已有（可选）
//	}
//	svc := service.NewCodexDegradationService(deps)
//	svc.Start(ctx)   // 启动自动检测循环（设置关闭时不产生探测）
//	// 进程退出时 svc.Stop()
//
// AccountRepo 若同时实现了 CodexDegradationDueAccountLister /
// CodexDegradationProbeResultStore，会自动启用 SQL 到期扫描与结果落库；
// 未实现时分别回退 ListByPlatform 内存过滤与跳过落库。
type CodexDegradationDeps struct {
	AccountRepo AccountRepository
	Settings    *SettingService
	Tokens      CodexDegradationTokenProvider
	RateLimiter CodexDegradationBlockClearer
	// Results 可选；nil 时尝试从 AccountRepo 做类型断言。
	Results CodexDegradationProbeResultStore
	// HTTPClient 可选；注入后探测请求走该客户端（测试用）。
	HTTPClient *http.Client
	// Endpoint 可选；覆盖上游 Codex responses 地址（测试用）。
	Endpoint string
	// Now 可选；测试可注入固定时钟。
	Now func() time.Time
	// Defaults 可选；由 config 包读取 gateway.openai_codex_degradation.* 后构造。
	Defaults *CodexDegradationSettings
	// ProbeTimeout 可选；单发自动探测超时，默认 120s。
	// 对应 config 的 gateway.openai_codex_degradation.probe_timeout_seconds。
	ProbeTimeout time.Duration
	// LeaderLock 可选；多实例部署时用于让自动检测只在 leader 上运行，
	// 避免每个实例重复探测同一批账号。nil 表示不做选举（单实例/测试）。
	LeaderLock LeaderLockCache
	// InstanceID 是 leader 锁的持有者标识；为空时由运行期用 pid 兜底。
	InstanceID string
}

// CodexDegradationService 提供 Codex 降智检测（ModelTrace 指纹）的全部能力：
// 账号原生探测、四态判定、自动降级检测与临时停调。
type CodexDegradationService struct {
	accountRepo    AccountRepository
	settingService *SettingService
	tokens         CodexDegradationTokenProvider
	rateLimiter    CodexDegradationBlockClearer
	results        CodexDegradationProbeResultStore
	httpClient     *http.Client
	endpoint       string
	now            func() time.Time
	defaults       *CodexDegradationSettings
	probeTimeout   time.Duration
	probe          codexDegradationProbe
	leaderLock     LeaderLockCache
	instanceID     string

	parentCtx    context.Context
	parentCancel context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	started      bool
	stopped      bool
	cycleMu      sync.Mutex
}

// NewCodexDegradationService 构造服务；不会自动启动后台循环。
func NewCodexDegradationService(deps CodexDegradationDeps) *CodexDegradationService {
	svc := &CodexDegradationService{
		accountRepo:    deps.AccountRepo,
		settingService: deps.Settings,
		tokens:         deps.Tokens,
		rateLimiter:    deps.RateLimiter,
		results:        deps.Results,
		httpClient:     deps.HTTPClient,
		endpoint:       strings.TrimSpace(deps.Endpoint),
		now:            deps.Now,
		defaults:       deps.Defaults,
		probeTimeout:   deps.ProbeTimeout,
		leaderLock:     deps.LeaderLock,
		instanceID:     deps.InstanceID,
	}
	if svc.endpoint == "" {
		svc.endpoint = chatgptCodexURL
	}
	if svc.probeTimeout <= 0 {
		svc.probeTimeout = codexDegradationProbeTimeout
	}
	if svc.results == nil {
		if store, ok := deps.AccountRepo.(CodexDegradationProbeResultStore); ok {
			svc.results = store
		}
	}
	// 自动检测默认用本服务自己的探测实现；测试可替换 svc.probe。
	svc.probe = svc
	return svc
}

// CodexNativeDiagnostic 是一次"号原生"降智探测的结果：请求由目标账号自身的
// 上游凭据发出，不经过公开网关，也不消耗任何用户的 API Key。
type CodexNativeDiagnostic struct {
	Output     string
	HTTPStatus int
	// Reason 非空表示本次探测没有拿到可评分的输出。
	Reason string
}

// RunNativeDiagnostic 用账号自己的凭据打一发 ModelTrace 探测。
// challenge 必须由调用方先生成，保证请求正文与最终判定用同一次抽签。
func (s *CodexDegradationService) RunNativeDiagnostic(ctx context.Context, accountID int64, model string, challenge ModelTraceChallenge, timeout time.Duration) CodexNativeDiagnostic {
	var result CodexNativeDiagnostic
	if s == nil || s.accountRepo == nil {
		result.Reason = "unavailable"
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		result.Reason = "identity_resolution_failed"
		return result
	}
	credential, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil || credential == nil {
		result.Reason = "identity_resolution_failed"
		return result
	}
	template, err := prepareCodexProbeTemplate(ctx, s.settingService)
	if err != nil {
		result.Reason = "template_invalid"
		return result
	}
	token, err := s.accessToken(ctx, credential)
	if err != nil || strings.TrimSpace(token) == "" {
		result.Reason = "credentials"
		return result
	}
	identity, err := newCodexProbeIdentityForAccount(credential, s.currentTime())
	if err != nil {
		result.Reason = "identity_resolution_failed"
		return result
	}
	if timeout <= 0 {
		timeout = codexDegradationProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, renderHeaders, err := template.renderCodexProbeRequest(model, challenge.Prompt, codexProbeTimezoneForAccount(credential), identity)
	if err != nil {
		result.Reason = "request"
		if errors.Is(err, ErrCodexProbeIdentity) || errors.Is(err, ErrCodexProbeTemplateInvalid) {
			result.Reason = "template_invalid"
		}
		return result
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		result.Reason = "request"
		return result
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	for name, values := range renderHeaders {
		req.Header[name] = values
	}
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(probeCtx, s.accountRepo, req.Header, credential); err != nil {
		result.Reason = "identity_resolution_failed"
		return result
	}
	applyOpenAICodexProbeHeaders(req.Header)
	enforceCodexIdentityHeadersWithUA(req.Header, credential.GetOpenAIUserAgent())
	// 探测身份里的 window id 必须与 body/client_metadata 一致，覆盖随机值。
	req.Header.Set("x-codex-window-id", identity.windowID)
	req.Header.Set("x-codex-turn-metadata", renderHeaders.Get("x-codex-turn-metadata"))

	proxyURL := ""
	if credential.ProxyID != nil && credential.Proxy != nil {
		proxyURL = credential.Proxy.URL()
	}
	resp, err := s.doProbeRequest(req, proxyURL, timeout)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			result.Reason = "timeout"
		} else {
			result.Reason = "request"
		}
		return result
	}
	if resp == nil {
		result.Reason = "request"
		return result
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	result.HTTPStatus = resp.StatusCode
	if resp.Body == nil {
		result.Reason = "response_incomplete"
		return result
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, codexProbeMaxBodyBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, codexProbeMaxBodyBytes))
	if readErr != nil {
		result.Reason = "request"
		return result
	}
	if resp.StatusCode != http.StatusOK {
		result.Reason = "http_status"
		return result
	}
	output := parseCodexProbeResponseOutput(raw)
	if strings.TrimSpace(output) == "" {
		result.Reason = "response_incomplete"
		return result
	}
	result.Output = output
	return result
}

func (s *CodexDegradationService) doProbeRequest(req *http.Request, proxyURL string, timeout time.Duration) (*http.Response, error) {
	if s != nil && s.httpClient != nil {
		return s.httpClient.Do(req)
	}
	client, err := httppool.GetClient(httppool.Options{
		ProxyURL:              proxyURL,
		Timeout:               timeout + 30*time.Second,
		ResponseHeaderTimeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// accessToken 取账号的上游 Bearer 凭据。OAuth 走 TokenProvider（自动刷新 +
// 缓存），setup-token 是 inference-only 凭据，直接读 credentials.access_token。
func (s *CodexDegradationService) accessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Type == AccountTypeSetupToken || account.IsOpenAIAgentIdentity() {
		token := strings.TrimSpace(account.GetOpenAIAccessToken())
		if token == "" {
			return "", errors.New("access_token not found in credentials")
		}
		return token, nil
	}
	if s != nil && s.tokens != nil {
		return s.tokens.GetAccessToken(ctx, account)
	}
	token := strings.TrimSpace(account.GetOpenAIAccessToken())
	if token == "" {
		return "", errors.New("access_token not found in credentials")
	}
	return token, nil
}

// parseCodexProbeResponseOutput 解析 Codex responses 的 SSE 流，返回模型文本。
// 优先取增量 delta；没有 delta 时回退 response.completed 里的 output_text。
// 对非 SSE 的 JSON 响应（测试/兼容上游）也做兜底解析。
func parseCodexProbeResponseOutput(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	var deltas strings.Builder
	var completed string
	sawEvent := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		sawEvent = true
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		eventType := gjson.Get(payload, "type").String()
		switch eventType {
		case "response.output_text.delta":
			_, _ = deltas.WriteString(gjson.Get(payload, "delta").String())
		case "response.completed", "response.done":
			completed += codexProbeCompletedOutputText(gjson.Get(payload, "response.output"))
		}
	}
	if deltas.Len() > 0 {
		return deltas.String()
	}
	if completed != "" {
		return completed
	}
	if sawEvent {
		return ""
	}
	// 非 SSE：直接按 Responses JSON 响应解析。
	if gjson.Valid(text) {
		if output := codexProbeCompletedOutputText(gjson.Get(text, "output")); output != "" {
			return output
		}
		if output := gjson.Get(text, "output_text"); output.Type == gjson.String {
			return output.String()
		}
	}
	return ""
}

func codexProbeCompletedOutputText(output gjson.Result) string {
	var text strings.Builder
	output.ForEach(func(_, item gjson.Result) bool {
		item.Get("content").ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "output_text" {
				_, _ = text.WriteString(part.Get("text").String())
			}
			return true
		})
		return true
	})
	return text.String()
}

// GetSettings 读取自动降智检测设置；key 缺失/损坏时返回默认值（关闭）。
func (s *CodexDegradationService) GetSettings(ctx context.Context) (*CodexDegradationSettings, error) {
	if s == nil {
		defaults := DefaultCodexDegradationSettings()
		return &defaults, nil
	}
	return s.getSettings(ctx)
}

// SaveSettings 校验并保存自动降智检测设置。校验失败不落库。
func (s *CodexDegradationService) SaveSettings(ctx context.Context, settings *CodexDegradationSettings) error {
	if err := ValidateCodexDegradationSettings(settings); err != nil {
		return err
	}
	if s == nil || s.settingService == nil {
		return errors.New("setting service unavailable")
	}
	return s.settingService.SetCodexDegradationSettings(ctx, settings)
}

// GetProbeTemplateRaw 读取可编辑的 JSONL 探测模板原文；空值表示使用内嵌默认模板。
func (s *CodexDegradationService) GetProbeTemplateRaw(ctx context.Context) (string, error) {
	if s == nil || s.settingService == nil {
		return DefaultCodexProbeTemplate(), nil
	}
	return s.settingService.GetCodexProbeTemplateRaw(ctx)
}

// SaveProbeTemplate 校验并保存 JSONL 探测模板。模板非法时拒绝保存（探测请求
// 同样会在发请求前拒绝）。
func (s *CodexDegradationService) SaveProbeTemplate(ctx context.Context, raw string) error {
	if _, err := ParseCodexProbeTemplate(raw); err != nil {
		return err
	}
	if s == nil || s.settingService == nil {
		return errors.New("setting service unavailable")
	}
	return s.settingService.SetCodexProbeTemplate(ctx, raw)
}

// DiagnosticModels 返回降智检测可选模型（指纹库 GPT 类 + 库缺类模型）。
func (s *CodexDegradationService) DiagnosticModels() ([]string, error) {
	return ModelTraceDiagnosticModels()
}

// Defaults 返回本服务生效的默认设置（config 覆盖优先）。
func (s *CodexDegradationService) Defaults() CodexDegradationSettings {
	if s == nil {
		return DefaultCodexDegradationSettings()
	}
	return s.defaultSettings()
}
