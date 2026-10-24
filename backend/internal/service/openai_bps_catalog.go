package service

import (
	"encoding/json"
	"strings"
)

// OpenAI BPS 模型目录：能力声明 + Codex manifest 构造。
//
// BPS 与 OpenAI 平台共享模型名族，但能力面完全不同：只有 Responses HTTP
// 协议、纯文本输入、串行工具调用；没有 WebSocket、图片输入、搜索工具、
// 并行工具调用与 reasoning summary / verbosity 参数。因此目录层不能复用
// "按模型名猜能力" 的 OpenAI 路径，而是用一份显式能力声明覆盖
// configuredCodexModelDescriptor 里由模型名触发的 OpenAI/Grok/Claude 分支。
const (
	// openAIBPSDefaultReasoningLevel 是 BPS 目录声明的默认推理档位。
	openAIBPSDefaultReasoningLevel = "medium"
	// openAIBPSModelDescription 是 BPS 目录条目的统一描述。
	openAIBPSModelDescription = "OpenAI BPS model routed through Sub2API."
)

// openAIBPSSupportedReasoningLevels 是 BPS 上游接受的推理档位（有序）。
var openAIBPSSupportedReasoningLevels = []string{"low", "medium", "high", "xhigh"}

// OpenAIBPSCatalogModel 是一个 BPS 公开模型的能力声明。
//
// 能力字段是目录的单一事实来源：manifest 构造与 Composite 目录投影都从这里
// 派生，避免两处各自猜测 BPS 能做什么。默认值由 OpenAIBPSCatalogModelForID
// 给出；自定义别名与默认模型共享同一份能力。
type OpenAIBPSCatalogModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// ContextWindow / CompactThreshold 来自 BPS 平台配置，是保守本地策略，
	// 不是上游配额承诺。
	ContextWindow    int `json:"context_window"`
	CompactThreshold int `json:"compact_threshold"`

	// SupportsToolCalls 为 true 且 SupportsParallelToolCalls 为 false：
	// BPS 只支持串行工具调用。
	SupportsToolCalls         bool `json:"supports_tool_calls"`
	SupportsParallelToolCalls bool `json:"supports_parallel_tool_calls"`
	// InputModalities 恒为 ["text"]：BPS 不接受图片输入。
	InputModalities []string `json:"input_modalities"`
	// 仅 Responses HTTP；WebSocket 明确关闭。
	SupportsHTTPResponses      bool `json:"supports_http_responses"`
	SupportsWebSocketResponses bool `json:"supports_websocket_responses"`

	SupportsSearchTool                bool `json:"supports_search_tool"`
	SupportsReasoningSummaryParameter bool `json:"supports_reasoning_summary_parameter"`
	SupportsVerbosity                 bool `json:"supports_verbosity"`

	DefaultReasoningLevel    string   `json:"default_reasoning_level"`
	SupportedReasoningLevels []string `json:"supported_reasoning_levels"`
}

// OpenAIBPSCatalogModelForID 返回某个公开模型名的 BPS 能力声明。
// 上下文与压缩阈值取自平台配置，其余能力是平台常量。
func OpenAIBPSCatalogModelForID(modelID string) OpenAIBPSCatalogModel {
	modelID = strings.TrimSpace(modelID)
	cfg := OpenAIBPSPlatformConfig()
	levels := make([]string, len(openAIBPSSupportedReasoningLevels))
	copy(levels, openAIBPSSupportedReasoningLevels)
	return OpenAIBPSCatalogModel{
		ID:          modelID,
		DisplayName: modelID,
		Description: openAIBPSModelDescription,
		// 配置加载器保证 ContextWindow > 0 且 0 < CompactThreshold < ContextWindow。
		ContextWindow:    cfg.ContextWindow,
		CompactThreshold: cfg.CompactThreshold,

		SupportsToolCalls:         true,
		SupportsParallelToolCalls: false,
		InputModalities:           []string{"text"},
		SupportsHTTPResponses:     true,
		// BPS 上游没有 Responses WebSocket 入口，客户端必须走 HTTP。
		SupportsWebSocketResponses: false,

		SupportsSearchTool:                false,
		SupportsReasoningSummaryParameter: false,
		SupportsVerbosity:                 false,

		DefaultReasoningLevel:    openAIBPSDefaultReasoningLevel,
		SupportedReasoningLevels: levels,
	}
}

// OpenAIBPSModelIDsForGroup 汇总分组内 BPS 账号可发布的公开模型名。
//
// 来源：
//   - 账号 model_mapping 的具体别名（含 "*" 的键是路由模式，不是模型名）；
//   - 映射目标值——仅当同一账号的白名单也接受该目标名时（例如通配符键
//     "gpt-*" 覆盖目标 "gpt-5.6-sol"），否则目标只存在于上游、客户端无法
//     以它作为公开模型请求；
//   - 平台配置 default_models——仅当至少一个 BPS 账号接受它时。空 mapping
//     等于允许全部（与 IsModelSupported 一致），所以未映射账号会得到默认
//     候选集；全部账号都配置了白名单时默认模型不会凭空出现。
//
// 非 BPS 账号完全排除（OpenAI/Grok 等同名模型不会混入 BPS 目录）；结果去重、
// 字典序稳定排序；分组白名单开启时再按白名单裁剪，与 /v1/models 的展示策略
// 保持一致。
func OpenAIBPSModelIDsForGroup(group *Group, accounts []Account) []string {
	models := openAIBPSAccountCatalogModels(accounts, true)
	for _, model := range OpenAIBPSDefaultModels() {
		if openAIBPSAccountsAcceptModel(accounts, model) {
			models = append(models, model)
		}
	}
	return FilterCodexModelIDsForGroup(dedupeAndSortModelIDs(models), group)
}

// BuildOpenAIBPSCodexModelsManifest 构造 BPS 专用的 Codex 模型目录。
//
// 输出与 BuildCodexModelsManifest 同形状（{"models":[{slug,...}]}），但每条
// 目的能力字段来自 OpenAIBPSCatalogModel：串行工具、纯文本、仅 HTTP，并显式
// 声明 prefer_websockets=false。空输入返回 {"models":[]}，不回退默认模型。
func BuildOpenAIBPSCodexModelsManifest(modelIDs []string) ([]byte, error) {
	models := make([]json.RawMessage, 0, len(modelIDs))
	seen := make(map[string]struct{}, len(modelIDs))
	for _, modelID := range modelIDs {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		// 与 BuildCodexModelsManifest 一致：媒体生成模型不是 Codex 对话模型。
		if isCodexDedicatedMediaModel(modelID) {
			continue
		}
		seen[modelID] = struct{}{}
		encoded, err := marshalOpenAIBPSCodexModel(modelID)
		if err != nil {
			return nil, err
		}
		models = append(models, encoded)
	}
	return json.Marshal(struct {
		Models []json.RawMessage `json:"models"`
	}{Models: models})
}

// BuildOpenAIBPSGroupCodexModelsManifest 组合模型发现与 manifest 构造：
// 先按分组账号/配置汇总公开模型名，再生成 BPS 能力目录。
func BuildOpenAIBPSGroupCodexModelsManifest(group *Group, accounts []Account) ([]byte, error) {
	return BuildOpenAIBPSCodexModelsManifest(OpenAIBPSModelIDsForGroup(group, accounts))
}

// openAIBPSAccountCatalogModels 枚举 BPS 账号的公开模型名。
//
// includeMappingTargets 控制是否把"账号白名单也接受的映射目标"计入：分组目录
// 需要（目标值可能是合法公开名），Composite 前缀筛选不需要（前缀路由只筛选
// 账号实际暴露的公开别名）。
func openAIBPSAccountCatalogModels(accounts []Account, includeMappingTargets bool) []string {
	candidates := make([]string, 0)
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAIBPS() {
			continue
		}
		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			continue
		}
		for alias, target := range mapping {
			alias = strings.TrimSpace(alias)
			if alias == "" || strings.Contains(alias, "*") {
				continue
			}
			candidates = append(candidates, alias)
			if !includeMappingTargets {
				continue
			}
			target = strings.TrimSpace(target)
			if target == "" || target == alias || strings.Contains(target, "*") {
				continue
			}
			if account.IsModelSupported(target) {
				candidates = append(candidates, target)
			}
		}
	}
	return dedupeAndSortModelIDs(candidates)
}

// openAIBPSAccountsAcceptModel 报告是否有 BPS 账号的白名单接受该模型名。
// 与选号器一致：空 mapping 接受全部，非空 mapping 按精确/通配符判定。
func openAIBPSAccountsAcceptModel(accounts []Account, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAIBPS() {
			continue
		}
		if account.IsModelSupported(model) {
			return true
		}
	}
	return false
}

// marshalOpenAIBPSCodexModel 生成单条 BPS Codex 目录项。
//
// 先复用 newConfiguredCodexModelDescriptor 得到与既有 manifest 完全一致的
// 字段集，再用能力声明覆盖所有"按模型名推断"的字段；官方 OpenAI 快照
// （officialMetadata）必须清空，否则 gpt-* 名字会把并行工具、图片输入等
// OpenAI 能力重新合并回来。
func marshalOpenAIBPSCodexModel(modelID string) (json.RawMessage, error) {
	catalogModel := OpenAIBPSCatalogModelForID(modelID)
	descriptor := newConfiguredCodexModelDescriptor(modelID)
	descriptor.officialMetadata = nil

	descriptor.DisplayName = catalogModel.DisplayName
	descriptor.Description = catalogModel.Description

	defaultReasoningLevel := catalogModel.DefaultReasoningLevel
	descriptor.DefaultReasoningLevel = &defaultReasoningLevel
	descriptor.MultiAgentReasoningEffort = &defaultReasoningLevel
	descriptor.SupportedReasoningLevels = make([]configuredCodexReasoningLevel, 0, len(catalogModel.SupportedReasoningLevels))
	for _, effort := range catalogModel.SupportedReasoningLevels {
		descriptor.SupportedReasoningLevels = append(descriptor.SupportedReasoningLevels, configuredCodexReasoningLevel{
			Effort:      effort,
			Description: configuredCodexReasoningLevelDescription(effort),
		})
	}
	// BPS 不支持 reasoning summary 参数；保留一个不触发该参数的中性默认值。
	descriptor.DefaultReasoningSummary = "none"

	descriptor.SupportsParallelToolCalls = catalogModel.SupportsParallelToolCalls
	descriptor.SupportsReasoningSummaryParameter = catalogModel.SupportsReasoningSummaryParameter
	descriptor.SupportVerbosity = catalogModel.SupportsVerbosity
	descriptor.SupportsSearchTool = catalogModel.SupportsSearchTool
	descriptor.SupportsImageDetailOriginal = false
	descriptor.InputModalities = append([]string(nil), catalogModel.InputModalities...)

	// 不支持 Fast/Ultrafast 等 OpenAI 服务档位。
	descriptor.AdditionalSpeedTiers = []string{}
	descriptor.ServiceTiers = []configuredCodexServiceTier{}
	descriptor.DefaultServiceTier = nil

	descriptor.ContextWindow = int64(catalogModel.ContextWindow)
	descriptor.MaxContextWindow = int64(catalogModel.ContextWindow)
	descriptor.AutoCompactTokenLimit = catalogModel.CompactThreshold

	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return nil, err
	}
	// configuredCodexModelDescriptor 没有 prefer_websockets 字段，但 BPS 必须
	// 明确告诉客户端不要尝试 WebSocket 升级，因此在 JSON 层补一个显式 false。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	fields["prefer_websockets"] = json.RawMessage("false")
	return json.Marshal(fields)
}
