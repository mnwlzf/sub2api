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

// PromptTemplateVersion 是发布后的不可变提示词版本。
//
// 所有内容字段在发布时一次性冻结；服务层拒绝编辑，数据库 trigger 进一步
// 阻止对已发布版本的 UPDATE/DELETE（见迁移 242）。因此一个 version_id
// 永远对应同一份正文与同一个 manifest_sha256，绑定与审计可以长期引用。
type PromptTemplateVersion struct {
	ent.Schema
}

func (PromptTemplateVersion) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "prompt_template_versions"},
	}
}

func (PromptTemplateVersion) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (PromptTemplateVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("template_id"),
		field.Int("version_no").
			Comment("模板内自增版本号，从 1 开始；(template_id, version_no) 唯一"),
		field.String("body").
			NotEmpty().
			Validate(validatePromptBodySize).
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("body_sha256").
			MaxLen(64).
			NotEmpty().
			Comment("正文原始 UTF-8 字节的 SHA-256"),
		field.Int("body_bytes").
			Comment("正文原始 UTF-8 字节数"),
		field.JSON("client_models", []string{}).
			Default(func() []string { return []string{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("upstream_models", []string{}).
			Default(func() []string { return []string{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("supported_profiles", []string{}).
			Default(func() []string { return []string{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("manifest_sha256").
			MaxLen(64).
			NotEmpty().
			Comment("对正文摘要、模型范围与 profile 清单确定性序列化后的 SHA-256"),
		field.String("change_note").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("idempotency_key").
			Optional().
			Nillable().
			MaxLen(64).
			Comment("发布幂等键；重放同一个键不会产生第二个版本（部分唯一索引）"),
		field.Int64("published_by").
			Optional().
			Nillable(),
		field.Time("published_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (PromptTemplateVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("template_id", "version_no").Unique(),
		index.Fields("template_id"),
		index.Fields("body_sha256"),
	}
}
