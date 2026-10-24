package config

import (
	"strings"

	"github.com/spf13/viper"
)

// 本文件只做「新增」：注册 gateway.openai_codex_ticket.* 的默认值，并提供
// 一个从全局 viper 读取该配置段的只读方法。刻意不修改 config.go：
//   - Config 结构体 / GatewayConfig 结构体由 Lead 在接线时决定是否补字段；
//   - 在字段落地之前，本文件的 init() 默认值 + CodexTicket() 读取方法即可
//     让服务层拿到完整配置，且不破坏 viper.Unmarshal 的既有行为
//     （未声明的默认值对 Unmarshal 是惰性的，不会产生未知字段错误）。

// OpenAI Codex 票据配置默认值。
const (
	// DefaultOpenAICodexTicketTargetLength 是官方 Codex 票据（x-codex-turn-state，
	// Fernet 形态）的默认字节长度：gAAAAA 前缀 + 292 字节。
	DefaultOpenAICodexTicketTargetLength = 292
	// OpenAICodexTicketTeamTargetLength 是 team / selfservebusinessprolite 账号的票据长度。
	OpenAICodexTicketTeamTargetLength = 332
	// DefaultOpenAICodexTicketTTLSeconds 是票据默认有效期（秒）。
	DefaultOpenAICodexTicketTTLSeconds = 3600
	// DefaultOpenAICodexTicketRefreshBeforeSeconds 是临期重打的提前量（秒）。
	DefaultOpenAICodexTicketRefreshBeforeSeconds = 600
	// DefaultOpenAICodexTicketProbeIntervalSeconds 是打票扫描周期（秒）。
	DefaultOpenAICodexTicketProbeIntervalSeconds = 6
	// DefaultOpenAICodexTicketAttemptTimeoutSeconds 是单次打票探测超时（秒）。
	DefaultOpenAICodexTicketAttemptTimeoutSeconds = 90
	// DefaultOpenAICodexTicketRetryMinSeconds / Max 是失败后的退避区间（秒）。
	DefaultOpenAICodexTicketRetryMinSeconds = 10
	DefaultOpenAICodexTicketRetryMaxSeconds = 30
	// DefaultOpenAICodexTicketRefreshSeconds 是成功票据的主动刷新周期（秒）。
	DefaultOpenAICodexTicketRefreshSeconds = 1800
)

// DefaultOpenAICodexTicketModels 是默认参与打票的模型列表。
// 与参考实现（sub4api）保持一致：只有会铸造 turn-state 的 GPT 系列模型参与。
var DefaultOpenAICodexTicketModels = []string{"gpt-6-astra", "gpt-5.6-sol"}

// CodexTicketConfig 是 gateway.openai_codex_ticket 的强类型视图。
// 字段命名与参考实现对齐，方便后续把配置真正挂到 GatewayConfig 上。
type CodexTicketConfig struct {
	// Enabled 是全局总开关，默认关闭。
	Enabled bool
	// TargetLength 是票据目标长度（字节），默认 292；team 账号会被规则覆盖为 332。
	TargetLength int
	// TTLSeconds 是票据有效期（秒）。
	TTLSeconds int
	// RefreshBeforeSeconds 是票据临期多久开始重打（秒）。
	RefreshBeforeSeconds int
	// HarvestProbeIntervalSeconds 是打票扫描周期（秒）。
	HarvestProbeIntervalSeconds int
	// HarvestAttemptTimeoutSeconds 是单次打票探测超时（秒）。
	HarvestAttemptTimeoutSeconds int
	// FailClosed 为 true 时，无票账号/模型会被门控为不可调度（默认 false）。
	FailClosed bool
	// Models 是参与打票的模型列表。
	Models []string
	// HarvestRetryMinSeconds / HarvestRetryMaxSeconds 是失败退避区间（秒）。
	HarvestRetryMinSeconds int
	HarvestRetryMaxSeconds int
	// HarvestRefreshSeconds 是成功票据的主动刷新周期（秒），0 表示不主动刷新。
	HarvestRefreshSeconds int
}

func init() {
	RegisterOpenAICodexTicketDefaults()
}

// RegisterOpenAICodexTicketDefaults 注册 gateway.openai_codex_ticket.* 默认值。
// 单独导出是为了让测试在 viper.Reset() 之后能重新注册（config 包其它测试会重置全局 viper）。
func RegisterOpenAICodexTicketDefaults() {
	// 全局默认关闭：即使后续代码全部就绪，也必须由管理员显式开启打票。
	viper.SetDefault("gateway.openai_codex_ticket.enabled", false)
	viper.SetDefault("gateway.openai_codex_ticket.target_length", DefaultOpenAICodexTicketTargetLength)
	viper.SetDefault("gateway.openai_codex_ticket.ttl_seconds", DefaultOpenAICodexTicketTTLSeconds)
	viper.SetDefault("gateway.openai_codex_ticket.refresh_before_seconds", DefaultOpenAICodexTicketRefreshBeforeSeconds)
	viper.SetDefault("gateway.openai_codex_ticket.harvest_probe_interval_seconds", DefaultOpenAICodexTicketProbeIntervalSeconds)
	viper.SetDefault("gateway.openai_codex_ticket.harvest_attempt_timeout_seconds", DefaultOpenAICodexTicketAttemptTimeoutSeconds)
	viper.SetDefault("gateway.openai_codex_ticket.fail_closed", false)
	viper.SetDefault("gateway.openai_codex_ticket.models", DefaultOpenAICodexTicketModels)
	viper.SetDefault("gateway.openai_codex_ticket.harvest_retry_min_seconds", DefaultOpenAICodexTicketRetryMinSeconds)
	viper.SetDefault("gateway.openai_codex_ticket.harvest_retry_max_seconds", DefaultOpenAICodexTicketRetryMaxSeconds)
	viper.SetDefault("gateway.openai_codex_ticket.harvest_refresh_seconds", DefaultOpenAICodexTicketRefreshSeconds)
}

// CodexTicket 返回 gateway.openai_codex_ticket 的当前生效配置。
// 读取全局 viper（config.Load 使用的就是全局实例），因此配置文件 / 环境变量
// 覆盖都能生效；Config 为 nil 时也能返回默认值，便于单测直接构造服务。
func (c *Config) CodexTicket() CodexTicketConfig {
	_ = c // 保留接收者：字段落地后可无痛切换为 c.Gateway.OpenAICodexTicket
	cfg := CodexTicketConfig{
		Enabled:                      viper.GetBool("gateway.openai_codex_ticket.enabled"),
		TargetLength:                 viper.GetInt("gateway.openai_codex_ticket.target_length"),
		TTLSeconds:                   viper.GetInt("gateway.openai_codex_ticket.ttl_seconds"),
		RefreshBeforeSeconds:         viper.GetInt("gateway.openai_codex_ticket.refresh_before_seconds"),
		HarvestProbeIntervalSeconds:  viper.GetInt("gateway.openai_codex_ticket.harvest_probe_interval_seconds"),
		HarvestAttemptTimeoutSeconds: viper.GetInt("gateway.openai_codex_ticket.harvest_attempt_timeout_seconds"),
		FailClosed:                   viper.GetBool("gateway.openai_codex_ticket.fail_closed"),
		Models:                       viper.GetStringSlice("gateway.openai_codex_ticket.models"),
		HarvestRetryMinSeconds:       viper.GetInt("gateway.openai_codex_ticket.harvest_retry_min_seconds"),
		HarvestRetryMaxSeconds:       viper.GetInt("gateway.openai_codex_ticket.harvest_retry_max_seconds"),
		HarvestRefreshSeconds:        viper.GetInt("gateway.openai_codex_ticket.harvest_refresh_seconds"),
	}
	return cfg
}

// NormalizeCodexTicketConfig 归一化越界值，返回可直接使用的配置。
// 服务层构造时调用一次；单测也可以直接用它断言默认值语义。
func NormalizeCodexTicketConfig(cfg CodexTicketConfig) CodexTicketConfig {
	if cfg.TargetLength <= 0 {
		cfg.TargetLength = DefaultOpenAICodexTicketTargetLength
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = DefaultOpenAICodexTicketTTLSeconds
	}
	if cfg.RefreshBeforeSeconds < 0 {
		cfg.RefreshBeforeSeconds = DefaultOpenAICodexTicketRefreshBeforeSeconds
	}
	if cfg.HarvestProbeIntervalSeconds <= 0 {
		cfg.HarvestProbeIntervalSeconds = DefaultOpenAICodexTicketProbeIntervalSeconds
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = DefaultOpenAICodexTicketAttemptTimeoutSeconds
	}
	if cfg.HarvestRetryMinSeconds <= 0 {
		cfg.HarvestRetryMinSeconds = DefaultOpenAICodexTicketRetryMinSeconds
	}
	if cfg.HarvestRetryMaxSeconds <= 0 {
		cfg.HarvestRetryMaxSeconds = DefaultOpenAICodexTicketRetryMaxSeconds
	}
	if cfg.HarvestRetryMaxSeconds < cfg.HarvestRetryMinSeconds {
		cfg.HarvestRetryMaxSeconds = cfg.HarvestRetryMinSeconds
	}
	if cfg.HarvestRefreshSeconds < 0 {
		cfg.HarvestRefreshSeconds = DefaultOpenAICodexTicketRefreshSeconds
	}
	models := make([]string, 0, len(cfg.Models))
	seen := make(map[string]struct{}, len(cfg.Models))
	for _, model := range cfg.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	if len(models) == 0 {
		models = append(models, DefaultOpenAICodexTicketModels...)
	}
	cfg.Models = models
	return cfg
}
