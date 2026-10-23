package config

import (
	"strings"

	"github.com/spf13/viper"
)

// OpenAI BPS（ChatGPT Excel/Basis Points 权益）平台配置。
//
// 与 Codex 打票/降智检测一致：默认值在 init 注册，配置结构由本文件按需读取，
// 不往 config.Config 里加字段，避免每加一个平台都要改主配置结构体。
const (
	OpenAIBPSConfigKeyEnabled          = "gateway.openai_bps.enabled"
	OpenAIBPSConfigKeyDefaultModels    = "gateway.openai_bps.default_models"
	OpenAIBPSConfigKeyContextWindow    = "gateway.openai_bps.context_window"
	OpenAIBPSConfigKeyCompactThreshold = "gateway.openai_bps.compact_threshold"
)

func init() {
	viper.SetDefault(OpenAIBPSConfigKeyEnabled, false)
	viper.SetDefault(OpenAIBPSConfigKeyDefaultModels, []string{"gpt-6-astra", "gpt-5.6-sol"})
	// 200k 上下文与 160k 压缩阈值是保守本地策略，不是上游配额承诺。
	viper.SetDefault(OpenAIBPSConfigKeyContextWindow, 200_000)
	viper.SetDefault(OpenAIBPSConfigKeyCompactThreshold, 160_000)
}

// OpenAIBPSConfig 是 BPS 平台运行期配置。
type OpenAIBPSConfig struct {
	// Enabled 是平台级 kill switch，默认关闭。
	Enabled bool
	// DefaultModels 是分组未显式配置时的候选模型。
	DefaultModels []string
	// ContextWindow / CompactThreshold 用于模型目录与压缩判定（保守值）。
	ContextWindow    int
	CompactThreshold int
}

// LoadOpenAIBPSConfig 从 viper 读取 BPS 配置并做边界归一。
func LoadOpenAIBPSConfig() OpenAIBPSConfig {
	cfg := OpenAIBPSConfig{
		Enabled:          viper.GetBool(OpenAIBPSConfigKeyEnabled),
		ContextWindow:    viper.GetInt(OpenAIBPSConfigKeyContextWindow),
		CompactThreshold: viper.GetInt(OpenAIBPSConfigKeyCompactThreshold),
	}
	cfg.DefaultModels = normalizeOpenAIBPSModels(viper.GetStringSlice(OpenAIBPSConfigKeyDefaultModels))
	if len(cfg.DefaultModels) == 0 {
		cfg.DefaultModels = []string{"gpt-6-astra", "gpt-5.6-sol"}
	}
	if cfg.ContextWindow <= 0 {
		cfg.ContextWindow = 200_000
	}
	if cfg.CompactThreshold <= 0 || cfg.CompactThreshold >= cfg.ContextWindow {
		cfg.CompactThreshold = cfg.ContextWindow * 4 / 5
	}
	return cfg
}

// normalizeOpenAIBPSModels 去空、去重并保持顺序。
func normalizeOpenAIBPSModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}
