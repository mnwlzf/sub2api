package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PromptAdminEvent 记录模板、版本、绑定与账号覆盖的管理操作。
//
// 正文不写入该表：只记录作用域 ID、前后状态摘要与操作者，避免普通管理日志
// 泄漏提示词内容。配置修改与管理事件在同一事务内写入。
type PromptAdminEvent struct {
	ent.Schema
}

func (PromptAdminEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "prompt_admin_events"},
	}
}

func (PromptAdminEvent) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (PromptAdminEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("action").
			MaxLen(40).
			NotEmpty().
			Comment("create/update_draft/publish/archive/bind/unbind/set_override/clear_override 等"),
		field.String("scope").
			MaxLen(20).
			NotEmpty().
			Validate(validatePromptAdminEventScope),
		field.Int64("template_id").
			Optional().
			Nillable(),
		field.Int64("version_id").
			Optional().
			Nillable(),
		field.Int64("group_id").
			Optional().
			Nillable(),
		field.Int64("account_id").
			Optional().
			Nillable(),
		field.Int64("actor_id").
			Optional().
			Nillable(),
		field.String("actor_name").
			Optional().
			Nillable().
			MaxLen(100),
		field.JSON("before_state", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("after_state", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("request_id").
			Optional().
			Nillable().
			MaxLen(64),
		field.String("note").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}

func (PromptAdminEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_at"),
		index.Fields("template_id"),
		index.Fields("group_id"),
		index.Fields("account_id"),
	}
}
