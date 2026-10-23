package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ProvideCodexDegradationService 构造并启动进程级 Codex 降智检测服务。
//
// Lead 接线方式（wire ProviderSet 里加一行 ProvideCodexDegradationService 即可）：
//   - accountRepo / settingService / openAITokenProvider / rateLimitService 都是
//     现有 wire 依赖，本函数负责把它们装配成 CodexDegradationDeps；
//   - 默认值来自 config 的 gateway.openai_codex_degradation.*（默认关闭）；
//   - 构造后立即 Start 后台循环：设置关闭时每轮 RunDue 立即返回，不产生探测流量。
//
// 进程退出时由 main 调用 Stop()（或依赖进程退出自然结束）。
func ProvideCodexDegradationService(
	accountRepo AccountRepository,
	settingService *SettingService,
	openAITokenProvider *OpenAITokenProvider,
	rateLimitService *RateLimitService,
	leaderLockCache LeaderLockCache,
	cfg *config.Config,
) *CodexDegradationService {
	// cfg 仅用于建立 wire 依赖顺序（确保 config.Load 已执行、viper 已就绪）。
	_ = cfg
	defaults := config.LoadCodexDegradationConfig()
	serviceDefaults := CodexDegradationSettings{
		Enabled:         defaults.Enabled,
		IntervalMinutes: defaults.IntervalMinutes,
		BlockMinutes:    defaults.BlockMinutes,
		Models:          append([]string(nil), defaults.Models...),
	}
	svc := NewCodexDegradationService(CodexDegradationDeps{
		AccountRepo:  accountRepo,
		Settings:     settingService,
		Tokens:       openAITokenProvider,
		RateLimiter:  rateLimitService,
		Defaults:     &serviceDefaults,
		ProbeTimeout: time.Duration(defaults.ProbeTimeoutSeconds) * time.Second,
		LeaderLock:   leaderLockCache,
		InstanceID:   codexDegradationInstanceID(),
	})
	svc.Start(context.Background())
	SetDefaultCodexDegradationRuntime(svc)
	return svc
}

// codexDegradationInstanceID 返回 leader 锁持有者标识（hostname-pid）。
// 失败时退化为 pid，保证锁仍能区分实例。
func codexDegradationInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return fmt.Sprintf("pid-%d", os.Getpid())
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}
