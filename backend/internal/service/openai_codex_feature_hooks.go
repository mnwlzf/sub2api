package service

import (
	"context"
	"net/http"
	"sync"
)

// 本文件定义 P2（打票）与 P4a（降智检测）子系统与网关主链路的运行期接口。
//
// 两个子系统都在独立文件里实现（见 codex_ticket_*.go 与 codex_degradation_*.go），
// 由 cmd/server 的装配层在构造 OpenAIGatewayService 之后注入；未注入时所有调用点
// 都是空操作，因此关闭功能（默认关闭）与完全不部署这两个子系统行为一致。
//
// 之所以用接口而不是具体类型：装配层可以选择只启用其中一个子系统，测试也可以
// 注入假实现，而不必把 repository/HTTP 依赖拖进 service 包。

// codexTicketRuntime 是 Codex 打票（x-codex-turn-state 票据）运行期接口。
type codexTicketRuntime interface {
	// Apply 在出站请求上补齐票据；无票且策略允许时返回 nil。
	Apply(ctx context.Context, account *Account, model string, h http.Header) error
	// Observe 捕获上游响应里的票据与基础设施 Cookie。
	Observe(ctx context.Context, account *Account, model string, respHeader http.Header)
	// BlocksAccount 报告当前账号/模型是否因无票策略被禁止调度。
	BlocksAccount(account *Account, model string) bool
}

// codexDegradationRuntime 是降智检测运行期接口。
type codexDegradationRuntime interface {
	// Start 启动自动检测运行器（后台 goroutine）；Stop 停止。
	Start(ctx context.Context)
	Stop()
}

// SetCodexTicketRuntime 注入打票运行时；传 nil 表示关闭。
func (s *OpenAIGatewayService) SetCodexTicketRuntime(runtime codexTicketRuntime) {
	if s == nil {
		return
	}
	s.codexTickets = runtime
}

// SetCodexDegradationRuntime 注入降智检测运行时；传 nil 表示关闭。
func (s *OpenAIGatewayService) SetCodexDegradationRuntime(runtime codexDegradationRuntime) {
	if s == nil {
		return
	}
	s.codexDegradation = runtime
}

// CodexDegradationRuntime 返回已注入的降智检测运行时（供装配层启动/路由使用）。
func (s *OpenAIGatewayService) CodexDegradationRuntime() codexDegradationRuntime {
	return s.resolveCodexDegradationRuntime()
}

// applyCodexTicketIfConfigured 在出站请求上补齐票据；未注入时直接返回。
func (s *OpenAIGatewayService) applyCodexTicketIfConfigured(ctx context.Context, account *Account, model string, h http.Header) error {
	runtime := s.resolveCodexTicketRuntime()
	if runtime == nil || account == nil || h == nil {
		return nil
	}
	return runtime.Apply(ctx, account, model, h)
}

// observeCodexTicketIfConfigured 捕获上游返回的票据；未注入时直接返回。
func (s *OpenAIGatewayService) observeCodexTicketIfConfigured(ctx context.Context, account *Account, model string, respHeader http.Header) {
	runtime := s.resolveCodexTicketRuntime()
	if runtime == nil || account == nil || respHeader == nil {
		return
	}
	runtime.Observe(ctx, account, model, respHeader)
}

// 进程级默认运行时。
//
// wire 装配顺序决定了 OpenAIGatewayService 可能先于打票/降智服务构造（后者依赖
// 网关的 GetAccessToken），无法在构造函数里互相注入。装配层在构造子系统后调用
// SetDefault* 注册；网关的 Apply/Observe 在实例字段为空时回退到默认运行时。
// 未注册时仍为空操作，因此测试与未部署子系统的场景行为不变。
var (
	defaultCodexRuntimeMu     sync.RWMutex
	defaultCodexTicketRT      codexTicketRuntime
	defaultCodexDegradationRT codexDegradationRuntime
)

// SetDefaultCodexTicketRuntime 注册进程级打票运行时（由装配层调用）。
func SetDefaultCodexTicketRuntime(runtime codexTicketRuntime) {
	defaultCodexRuntimeMu.Lock()
	defer defaultCodexRuntimeMu.Unlock()
	defaultCodexTicketRT = runtime
}

// SetDefaultCodexDegradationRuntime 注册进程级降智检测运行时（由装配层调用）。
func SetDefaultCodexDegradationRuntime(runtime codexDegradationRuntime) {
	defaultCodexRuntimeMu.Lock()
	defer defaultCodexRuntimeMu.Unlock()
	defaultCodexDegradationRT = runtime
}

func (s *OpenAIGatewayService) resolveCodexTicketRuntime() codexTicketRuntime {
	if s != nil && s.codexTickets != nil {
		return s.codexTickets
	}
	defaultCodexRuntimeMu.RLock()
	defer defaultCodexRuntimeMu.RUnlock()
	return defaultCodexTicketRT
}

func (s *OpenAIGatewayService) resolveCodexDegradationRuntime() codexDegradationRuntime {
	if s != nil && s.codexDegradation != nil {
		return s.codexDegradation
	}
	defaultCodexRuntimeMu.RLock()
	defer defaultCodexRuntimeMu.RUnlock()
	return defaultCodexDegradationRT
}
