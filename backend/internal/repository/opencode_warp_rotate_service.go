package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	// openCodeWarpRotateEnsureFreshPath 是轮换服务的「确保出口可用」端点。
	// 语义：当前出口可用则直接返回（action=none，不轮换），不可用才轮换到可用为止。
	openCodeWarpRotateEnsureFreshPath = "/ensure-fresh"
	// openCodeWarpRotateResponseMaxBytes 限制响应读取量，避免异常响应打爆内存。
	openCodeWarpRotateResponseMaxBytes = int64(64 * 1024)
	// openCodeWarpRotateErrorDetailMaxBytes 错误信息入日志前的截断长度。
	openCodeWarpRotateErrorDetailMaxBytes = 512
	// defaultOpenCodeWarpRotateTimeout 与 service 侧默认值保持一致：
	// 轮换服务自身硬超时约 90 秒，客户端必须略大于它。
	defaultOpenCodeWarpRotateTimeout = 95 * time.Second
)

// openCodeWarpRotateResponse 是轮换服务 200 / 503 响应体的联合形状。
//
//	200 → {"ok":true,"action":"none"|"rotated","usable":true,"exit_ip":"...",...}
//	503 → {"ok":false,"usable":false,"error":"no_usable_exit","retry_after":49711}
type openCodeWarpRotateResponse struct {
	OK       bool   `json:"ok"`
	Action   string `json:"action"`
	Usable   bool   `json:"usable"`
	ExitIP   string `json:"exit_ip"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error"`
	// RetryAfter 是服务端给出的建议重试间隔（秒），仅用于错误诊断。
	RetryAfter int64 `json:"retry_after"`
}

// openCodeWarpRotateService 通过 HTTP 调用外部 WARP 出口轮换服务。
type openCodeWarpRotateService struct {
	serviceURL string
	token      string
	client     *http.Client
}

// NewOpenCodeWarpRotator 构造 WARP 出口轮换器。
//
// 未配置 service_url（或 cfg 为空）时返回 nil：调用方不注入，整个能力保持 no-op，
// 与改动前的行为完全一致。这是「合并即安全」的兜底。
func NewOpenCodeWarpRotator(cfg *config.Config) service.WarpExitRotator {
	if cfg == nil {
		return nil
	}
	settings := cfg.Gateway.OpenCodeWarpRotate
	serviceURL := strings.TrimRight(strings.TrimSpace(settings.ServiceURL), "/")
	if serviceURL == "" {
		return nil
	}
	timeout := time.Duration(settings.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultOpenCodeWarpRotateTimeout
	}
	// 显式清空 Transport.Proxy：这是站内服务调用，不能继承 HTTP_PROXY/HTTPS_PROXY
	// 环境变量，否则会被运维侧的出网代理劫持（轮换服务通常只在容器网络内可达）。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &openCodeWarpRotateService{
		serviceURL: serviceURL,
		token:      strings.TrimSpace(settings.Token),
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

// EnsureFreshExit 调用轮换服务，确保 proxyID 背后存在一个可用出口。
//
// 只有「HTTP 200 且 ok=true 且 usable=true」才算成功；503（no_usable_exit）等
// 情况一律返回 error，调用方据此放弃同账号重试、走原有 failover 链路。
func (s *openCodeWarpRotateService) EnsureFreshExit(ctx context.Context, proxyID int64, proxyURL string) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("opencode warp rotate: client not configured")
	}
	if proxyID <= 0 {
		return "", fmt.Errorf("opencode warp rotate: invalid proxy_id %d", proxyID)
	}
	// proxyURL 只用于诊断，轮换服务只需要 proxy_id：端点不变，变的是出口 IP。
	_ = proxyURL

	payload, err := json.Marshal(map[string]any{"proxy_id": proxyID})
	if err != nil {
		return "", fmt.Errorf("opencode warp rotate: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.serviceURL+openCodeWarpRotateEnsureFreshPath,
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", fmt.Errorf("opencode warp rotate: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("opencode warp rotate: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, openCodeWarpRotateResponseMaxBytes))
	if readErr != nil {
		return "", fmt.Errorf("opencode warp rotate: read response: %w", readErr)
	}
	var parsed openCodeWarpRotateResponse
	if len(bytes.TrimSpace(body)) > 0 {
		if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
			return "", fmt.Errorf(
				"opencode warp rotate: decode response (status %d): %w",
				resp.StatusCode, jsonErr,
			)
		}
	}
	if resp.StatusCode != http.StatusOK || !parsed.OK || !parsed.Usable {
		reason := strings.TrimSpace(parsed.Error)
		if reason == "" {
			reason = strings.TrimSpace(string(body))
		}
		if len(reason) > openCodeWarpRotateErrorDetailMaxBytes {
			reason = reason[:openCodeWarpRotateErrorDetailMaxBytes]
		}
		return "", fmt.Errorf(
			"opencode warp rotate: unusable exit (status %d, ok=%t, usable=%t, action=%q, attempts=%d, retry_after=%d): %s",
			resp.StatusCode, parsed.OK, parsed.Usable, parsed.Action, parsed.Attempts, parsed.RetryAfter, reason,
		)
	}
	return strings.TrimSpace(parsed.ExitIP), nil
}
