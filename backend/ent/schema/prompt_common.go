package schema

import "fmt"

// 文本提示词功能（prompt_* 表）共享的枚举校验。
//
// 设计取舍：这些表只声明普通 int64 外键列，不声明 Ent edge。
// 原因是 group_prompt_bindings / account_group_prompt_overrides 需要
// 复合外键（account_group_prompt_overrides.(account_id, group_id) ->
// account_groups(account_id, group_id)）与部分唯一索引，这些约束由手写
// SQL 迁移表达更直接；声明 Ent edge 会改动 account.go / group.go 并引入
// 无法表达复合外键的抽象。查询只需要按 id 精确查找，不需要图遍历。

// 分组绑定模式：disabled（关闭）或 version（绑定某个固定版本）。
var promptBindingModes = map[string]struct{}{
	"disabled": {},
	"version":  {},
}

// 账号级覆盖模式：inherit（继承分组）、disabled（该分组内关闭）、version（覆盖版本）。
var promptOverrideModes = map[string]struct{}{
	"inherit":  {},
	"disabled": {},
	"version":  {},
}

func validatePromptBindingMode(value string) error {
	if _, ok := promptBindingModes[value]; ok {
		return nil
	}
	return fmt.Errorf("invalid prompt binding mode %q", value)
}

func validatePromptOverrideMode(value string) error {
	if _, ok := promptOverrideModes[value]; ok {
		return nil
	}
	return fmt.Errorf("invalid prompt override mode %q", value)
}

// 管理审计事件的作用域。
var promptAdminEventScopes = map[string]struct{}{
	"template":         {},
	"version":          {},
	"binding":          {},
	"account_override": {},
}

func validatePromptAdminEventScope(value string) error {
	if _, ok := promptAdminEventScopes[value]; ok {
		return nil
	}
	return fmt.Errorf("invalid prompt admin event scope %q", value)
}

// 提示词正文上限（字节）。支持基础人格与单个完整 Skill 的静态组合。
const promptBodyMaxBytes = 64 * 1024

func validatePromptBodySize(value string) error {
	if len(value) == 0 {
		return fmt.Errorf("prompt body must not be empty")
	}
	if len(value) > promptBodyMaxBytes {
		return fmt.Errorf("prompt body exceeds %d bytes", promptBodyMaxBytes)
	}
	return nil
}
