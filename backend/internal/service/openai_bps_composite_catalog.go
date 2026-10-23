package service

import (
	"sort"
	"strings"
)

// BuildOpenAIBPSCompositeCatalogEntries 把 BPS 相关的 Composite 路由投影成
// BPS 能力目录条目。调用方（Composite 的 /v1/models 或 Codex manifest）负责
// 与其它平台的条目合并；本函数只回答"BPS 能发布哪些公开模型"。
//
// 规则：
//   - 只处理 Enabled 且 target_platform=openai_bps 的路由；BPS 不是
//     DetectModelPlatform 的推导结果，必须显式路由；
//   - endpoint 只接受 any/responses（BPS 只有 Responses HTTP 入口）；
//   - exact 路由直接发布 public_model 别名，upstream_model 缺省时回填别名；
//   - prefix 路由不合成模型名，只在"可枚举的 BPS 账号模型"（账号 mapping
//     别名 + 未映射账号可用的平台默认模型）里筛选前缀命中项；
//   - 每条目都要求至少一个 BPS 账号的白名单接受其最终上游模型名。Composite
//     中间件会把 body 里的 model 改写成 upstream_model，BPS 选号器随后按改写后
//     的模型名做 IsModelSupported 判定；不满足这条的别名发布出去必然 409；
//   - 无候选返回空切片，绝不回退到其它平台的检测器或默认模型。
func BuildOpenAIBPSCompositeCatalogEntries(routes []CompositeModelRoute, accounts []Account) []OpenAIBPSCatalogModel {
	enumerable := openAIBPSAccountCatalogModels(accounts, false)
	for _, model := range OpenAIBPSDefaultModels() {
		if openAIBPSAccountsAcceptModel(accounts, model) {
			enumerable = append(enumerable, model)
		}
	}
	enumerable = dedupeAndSortModelIDs(enumerable)

	entries := make([]OpenAIBPSCatalogModel, 0)
	seen := make(map[string]struct{})
	add := func(publicModel string) {
		if _, exists := seen[publicModel]; exists {
			return
		}
		seen[publicModel] = struct{}{}
		entries = append(entries, OpenAIBPSCatalogModelForID(publicModel))
	}

	for _, route := range routes {
		if !route.Enabled || strings.TrimSpace(route.TargetPlatform) != PlatformOpenAIBPS {
			continue
		}
		endpoint := normalizeCompositeRouteEndpoint(route.Endpoint)
		if endpoint != CompositeRouteEndpointAny && endpoint != CompositeRouteEndpointResponses {
			continue
		}
		publicModel := strings.TrimSpace(route.PublicModel)
		if publicModel == "" {
			continue
		}
		upstreamModel := strings.TrimSpace(route.UpstreamModel)

		switch normalizeCompositeRouteMatchType(route.MatchType) {
		case CompositeRouteMatchExact:
			target := upstreamModel
			if target == "" {
				target = publicModel
			}
			if !openAIBPSAccountsAcceptModel(accounts, target) {
				continue
			}
			add(publicModel)
		case CompositeRouteMatchPrefix:
			for _, candidate := range enumerable {
				if !strings.HasPrefix(candidate, publicModel) {
					continue
				}
				target := upstreamModel
				if target == "" {
					target = candidate
				}
				if !openAIBPSAccountsAcceptModel(accounts, target) {
					continue
				}
				add(candidate)
			}
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ID < entries[j].ID
	})
	return entries
}
