package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AccountGroupPromptOverride 是“账号在某个分组内”的提示词覆盖。
//
// 作用域是 account_groups 关系而不是账号全局字段：同一个账号同时属于 A、B
// 两个分组时，在 A 组内的覆盖不会影响 B 组。缺少该行等价于 mode=inherit。
type AccountGroupPromptOverride struct {
	ent.Schema
}

func (AccountGroupPromptOverride) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "account_group_prompt_overrides"},
	}
}

func (AccountGroupPromptOverride) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (AccountGroupPromptOverride) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("account_id"),
		field.Int64("group_id"),
		field.String("mode").
			MaxLen(20).
			NotEmpty().
			Default("inherit").
			Validate(validatePromptOverrideMode),
		field.Int64("version_id").
			Optional().
			Nillable().
			Comment("mode=version 时必填；数据库 CHECK 保证 mode 与 version_id 配套"),
		field.Int("revision").
			Default(1),
		field.Int64("updated_by").
			Optional().
			Nillable(),
	}
}

func (AccountGroupPromptOverride) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account_id", "group_id").Unique(),
		index.Fields("group_id"),
		index.Fields("version_id"),
	}
}
