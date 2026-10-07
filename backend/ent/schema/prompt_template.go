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

// PromptTemplate 是文本提示词的中央模板（元数据 + 生命周期）。
//
// 模板本身不保存正文：可编辑正文放在 PromptTemplateDraft，不可变正文放在
// PromptTemplateVersion。这样“编辑草稿”和“已发布版本”不会互相污染，
// 分组绑定的永远是一个内容不可变的版本。
type PromptTemplate struct {
	ent.Schema
}

func (PromptTemplate) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "prompt_templates"},
	}
}

func (PromptTemplate) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (PromptTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			MaxLen(100).
			NotEmpty().
			Comment("模板名称；活动模板（archived_at IS NULL）内唯一"),
		field.String("description").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("source_url").
			Optional().
			Nillable().
			MaxLen(500).
			Comment("候选来源 URL，仅记录，网关不会自动下载"),
		field.String("source_note").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Comment("来源与适用范围说明"),
		field.Time("archived_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("归档时间；归档后不能新增绑定，既有绑定继续运行"),
		field.Int("revision").
			Default(1).
			Comment("乐观锁版本，每次元数据更新递增"),
		field.Int64("created_by").
			Optional().
			Nillable(),
		field.Int64("updated_by").
			Optional().
			Nillable(),
	}
}

func (PromptTemplate) Indexes() []ent.Index {
	return []ent.Index{
		// 实际数据库中是部分唯一索引：WHERE archived_at IS NULL（见迁移 242）。
		index.Fields("name"),
		index.Fields("archived_at"),
	}
}
