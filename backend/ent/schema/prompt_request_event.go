package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PromptRequestEvent 记录一次“逻辑请求 × 上游尝试”的提示词策略决策。
//
// 目的：让后台能回答“这个请求到底注入了没有、为什么没注入、重试时是否换了策略”。
// 只记录策略身份与结果，不记录正文、不记录客户请求体，避免普通运行日志泄漏提示词。
//
// 写入是尽力而为：记录失败只打日志/指标，绝不因此重试已经成功的上游请求。
type PromptRequestEvent struct {
	ent.Schema
}

func (PromptRequestEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "prompt_request_events"},
	}
}

func (PromptRequestEvent) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (PromptRequestEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("request_id").
			Optional().
			Nillable().
			MaxLen(64),
		field.Int("attempt_no").
			Default(1).
			Comment("同一逻辑请求内的第几次上游尝试，从 1 开始"),
		field.Int64("group_id").
			Optional().
			Nillable(),
		field.Int64("account_id").
			Optional().
			Nillable(),
		field.String("client_model").
			Optional().
			Nillable().
			MaxLen(120),
		field.String("upstream_model").
			Optional().
			Nillable().
			MaxLen(120),
		field.String("outbound_profile").
			Optional().
			Nillable().
			MaxLen(40).
			Comment("实际出站协议 profile；空表示该路径不在支持范围内"),
		field.String("binding_source").
			Optional().
			Nillable().
			MaxLen(20).
			Comment("group 或 account_override"),
		field.Int64("version_id").
			Optional().
			Nillable(),
		field.String("manifest_sha256").
			Optional().
			Nillable().
			MaxLen(64),
		field.Bool("applied").
			Default(false).
			Comment("本次尝试是否真的写入了提示词"),
		field.String("reason").
			MaxLen(40).
			NotEmpty().
			Comment("固定枚举，见 service.PromptReason*"),
		field.Int("added_bytes").
			Default(0).
			Comment("注入新增的字节数；未注入为 0"),
		field.Int("apply_duration_ms").
			Default(0),
	}
}

func (PromptRequestEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_at"),
		index.Fields("request_id"),
		index.Fields("group_id"),
		index.Fields("version_id"),
		index.Fields("applied"),
	}
}
