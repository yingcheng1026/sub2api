package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SubscriptionPlanGroup 钱包模式 plan ↔ group 多对多关联（v4）。
//
// 模式判别：
//   - 老 v3 单 group 订阅：subscription_plans.group_id NOT NULL，本表无关联
//   - 新 v4 钱包模式：    subscription_plans.group_id NULL，本表 N 行关联
//
// 详细设计：ai-relay-infra/docs/plans/2026-05-10-wallet-mode-design.md §1.1
type SubscriptionPlanGroup struct {
	ent.Schema
}

func (SubscriptionPlanGroup) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "subscription_plan_groups"},
	}
}

func (SubscriptionPlanGroup) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("plan_id"),
		field.Int64("group_id"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SubscriptionPlanGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("plan", SubscriptionPlan.Type).
			Ref("plan_groups").
			Field("plan_id").
			Unique().
			Required(),
		edge.From("group", Group.Type).
			Ref("plan_groups").
			Field("group_id").
			Unique().
			Required(),
	}
}

func (SubscriptionPlanGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("plan_id", "group_id").Unique(),
		index.Fields("group_id"),
	}
}
