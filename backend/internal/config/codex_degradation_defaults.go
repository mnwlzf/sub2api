package config

import (
	"strings"

	"github.com/spf13/viper"
)

// Codex 降智检测（ModelTrace 指纹）的配置键前缀。
//
// 该功能只在管理面显式开启后才产生上游探测流量，因此所有键都注册"关闭/安全"
// 默认值：viper 只有出现在 AllKeys() 里的键才会被环境变量覆盖（见
// env_reachability_test.go 的系统性说明），在 init() 注册默认值是让
// GATEWAY_OPENAI_CODEX_DEGRADATION_* 环境变量真正可达的必要步骤。
const (
	codexDegradationConfigPrefix = "gateway.openai_codex_degradation"

	// CodexDegradationConfigKeyEnabled 是否启用自动降智检测（默认关闭）。
	CodexDegradationConfigKeyEnabled = codexDegradationConfigPrefix + ".enabled"
	// CodexDegradationConfigKeyIntervalMinutes 自动检测间隔（分钟，默认 10）。
	CodexDegradationConfigKeyIntervalMinutes = codexDegradationConfigPrefix + ".interval_minutes"
	// CodexDegradationConfigKeyBlockMinutes 判定降智后的临时停调时长（分钟，默认 30）。
	CodexDegradationConfigKeyBlockMinutes = codexDegradationConfigPrefix + ".block_minutes"
	// CodexDegradationConfigKeyModels 自动检测的模型列表（默认 gpt-6.1-sol）。
	CodexDegradationConfigKeyModels = codexDegradationConfigPrefix + ".models"
	// CodexDegradationConfigKeyProbeTimeoutSeconds 单发探测超时（秒，默认 120）。
	CodexDegradationConfigKeyProbeTimeoutSeconds = codexDegradationConfigPrefix + ".probe_timeout_seconds"
	// CodexDegradationConfigKeyProbeTemplateFile 可选的 JSONL 探测模板文件路径。
	// 空值表示使用内嵌默认模板；设置项（openai_codex_probe_template）优先。
	CodexDegradationConfigKeyProbeTemplateFile = codexDegradationConfigPrefix + ".probe_template_file"
)

func init() {
	viper.SetDefault(CodexDegradationConfigKeyEnabled, false)
	viper.SetDefault(CodexDegradationConfigKeyIntervalMinutes, 10)
	viper.SetDefault(CodexDegradationConfigKeyBlockMinutes, 30)
	viper.SetDefault(CodexDegradationConfigKeyModels, []string{"gpt-6.1-sol"})
	viper.SetDefault(CodexDegradationConfigKeyProbeTimeoutSeconds, 120)
	viper.SetDefault(CodexDegradationConfigKeyProbeTemplateFile, "")
}

// CodexDegradationConfig 是 gateway.openai_codex_degradation.* 的只读快照。
// 由 main.go 转成 service.CodexDegradationSettings 作为服务默认值。
type CodexDegradationConfig struct {
	Enabled             bool
	IntervalMinutes     int
	BlockMinutes        int
	Models              []string
	ProbeTimeoutSeconds int
	ProbeTemplateFile   string
}

// LoadCodexDegradationConfig 读取降智检测配置；键缺失时使用内置默认值。
// 不依赖 viper 默认值是否被 Reset，因此测试/工具进程同样安全。
func LoadCodexDegradationConfig() CodexDegradationConfig {
	cfg := CodexDegradationConfig{
		Enabled:             false,
		IntervalMinutes:     10,
		BlockMinutes:        30,
		Models:              []string{"gpt-6.1-sol"},
		ProbeTimeoutSeconds: 120,
	}
	if viper.IsSet(CodexDegradationConfigKeyEnabled) {
		cfg.Enabled = viper.GetBool(CodexDegradationConfigKeyEnabled)
	}
	if viper.IsSet(CodexDegradationConfigKeyIntervalMinutes) {
		if value := viper.GetInt(CodexDegradationConfigKeyIntervalMinutes); value > 0 {
			cfg.IntervalMinutes = value
		}
	}
	if viper.IsSet(CodexDegradationConfigKeyBlockMinutes) {
		if value := viper.GetInt(CodexDegradationConfigKeyBlockMinutes); value > 0 {
			cfg.BlockMinutes = value
		}
	}
	if viper.IsSet(CodexDegradationConfigKeyModels) {
		if models := normalizeCodexDegradationConfigModels(viper.GetStringSlice(CodexDegradationConfigKeyModels)); len(models) > 0 {
			cfg.Models = models
		}
	}
	if viper.IsSet(CodexDegradationConfigKeyProbeTimeoutSeconds) {
		if value := viper.GetInt(CodexDegradationConfigKeyProbeTimeoutSeconds); value > 0 {
			cfg.ProbeTimeoutSeconds = value
		}
	}
	cfg.ProbeTemplateFile = strings.TrimSpace(viper.GetString(CodexDegradationConfigKeyProbeTemplateFile))
	return cfg
}

func normalizeCodexDegradationConfigModels(models []string) []string {
	result := make([]string, 0, len(models))
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
		result = append(result, model)
	}
	return result
}
