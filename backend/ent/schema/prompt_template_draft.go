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

// PromptTemplateDraft 是一个模板当前唯一的可编辑草稿。
//
// 草稿可以反复修改，只有“发布”才会把草稿快照成不可变的
// PromptTemplateVersion。发布必须携带 draft_revision，避免两个管理员
// 同时编辑时后提交者静默覆盖前者。
type PromptTemplateDraft struct {
	ent.Schema
}

func (PromptTemplateDraft) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "prompt_template_drafts"},
	}
}

func (PromptTemplateDraft) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (PromptTemplateDraft) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("template_id").
			Comment("所属模板，一个模板最多一个草稿"),
		field.String("body").
			NotEmpty().
			Validate(validatePromptBodySize).
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Comment("提示词正文，UTF-8，上限 64 KiB"),
		field.JSON("client_models", []string{}).
			Default(func() []string { return []string{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("适用的客户端模型名（精确匹配）；空集合不等于全选"),
		field.JSON("upstream_models", []string{}).
			Default(func() []string { return []string{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("适用的上游模型名（模型映射后精确匹配）；空集合不等于全选"),
		field.JSON("supported_profiles", []string{}).
			Default(func() []string { return []string{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("允许的协议适配 profile ID 集合"),
		field.Int("revision").
			Default(1).
			Comment("草稿乐观锁版本，发布与更新都校验该值"),
		field.Int64("updated_by").
			Optional().
			Nillable(),
	}
}

func (PromptTemplateDraft) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("template_id").Unique(),
	}
}
