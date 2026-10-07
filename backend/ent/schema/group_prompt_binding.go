package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// GroupPromptBinding 决定一个分组是否启用文本提示词注入，以及绑定哪个固定版本。
//
// 缺少该行等价于 mode=disabled：新分组、复制出来的分组都不会意外启用候选。
// 分组关闭是该分组内所有账号的总开关，账号级覆盖不能绕过它。
type GroupPromptBinding struct {
	ent.Schema
}

func (GroupPromptBinding) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "group_prompt_bindings"},
	}
}

func (GroupPromptBinding) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (GroupPromptBinding) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("group_id").
			Comment("分组 ID，一个分组最多一行绑定"),
		field.String("mode").
			MaxLen(20).
			NotEmpty().
			Default("disabled").
			Validate(validatePromptBindingMode),
		field.Int64("version_id").
			Optional().
			Nillable().
			Comment("mode=version 时必填；数据库 CHECK 保证 mode 与 version_id 配套"),
		field.Int("revision").
			Default(1).
			Comment("乐观锁版本"),
		field.Int64("updated_by").
			Optional().
			Nillable(),
	}
}

func (GroupPromptBinding) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id").Unique(),
		index.Fields("version_id"),
	}
}
