package service

// BPS 轮次身份（turn_id / agent_iteration）。
//
// 设计约束（见 openspec design.md D7）：
//   - turn_id 由不可变历史派生，重试同一请求绝不推进任何可变计数器；
//   - 压缩记录同时保存 turn_id 与迭代基数，恢复后若没有新的用户消息则保持
//     同一轮次，只按新增的工具结果递增 agent_iteration；
//   - 旧压缩记录没有 turn_id 时用 scope+引用派生稳定值，保证跨实例一致。
import (
	"strings"

	"github.com/google/uuid"
)

// openAIBPSTurnState 从原始客户端历史计算本轮 turn_id 与 agent_iteration。
//
// input 是客户端原始 input 数组（不是翻译后的上游 input）：压缩 item 仍然
// 以引用形式存在，工具结果以 function_call_output/custom_tool_call_output
// 形式存在。函数是纯函数：同样的 input 与压缩记录必然得到同样的结果，因此
// 重试相同请求不会改变上游 body。
//
// compactIndex/compactRef/compact 描述历史里最近一条可恢复的压缩引用：
// 当它之后没有新的用户消息时，沿用记录里的轮次锚点与迭代基数。
func openAIBPSTurnState(scope string, input []any, compactIndex int, compactRef string, compact openAIBPSCompactState) (string, int) {
	lastUser := -1
	for index, raw := range input {
		if item, ok := raw.(map[string]any); ok && stringValue(item["role"]) == "user" {
			lastUser = index
		}
	}
	iteration := 1
	start := lastUser + 1
	turnID := ""
	if compactIndex >= 0 && lastUser <= compactIndex {
		// 压缩之后没有新的用户消息：保持原轮次；旧记录缺 turn_id 时派生稳定值。
		turnID = strings.TrimSpace(compact.TurnID)
		if turnID == "" {
			turnID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(scope+":compact:"+compactRef)).String()
		}
		if compact.AgentIteration > 0 {
			iteration = compact.AgentIteration
		}
		start = compactIndex + 1
	} else {
		// 新轮次：只取到最后一条用户消息（含）为止的前缀，后续工具往返不会
		// 改变 turn_id。没有用户消息时退化为首条 item，保持重试稳定。
		prefix := input
		if lastUser >= 0 {
			prefix = input[:lastUser+1]
		} else if len(prefix) > 1 {
			prefix = prefix[:1]
		}
		turnID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(scope+":"+openAIBPSJSON(prefix))).String()
	}
	for _, raw := range input[start:] {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(item["type"]) {
		case "function_call_output", "custom_tool_call_output":
			iteration++
		}
	}
	return turnID, iteration
}
