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

// SubscriptionPlan holds the schema definition for the SubscriptionPlan entity.
//
// 删除策略：硬删除
// SubscriptionPlan 使用硬删除而非软删除，原因如下：
//   - 套餐为管理员维护的商品配置，删除即表示下架移除
//   - 通过 for_sale 字段控制是否在售，删除仅用于彻底移除
//   - 已购买的订阅记录保存在 UserSubscription 中，不受套餐删除影响
type SubscriptionPlan struct {
	ent.Schema
}

func (SubscriptionPlan) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "subscription_plans"},
	}
}

func (SubscriptionPlan) Fields() []ent.Field {
	return []ent.Field{
		// group_id 老 v3 单 group 订阅模式下必填；钱包模式（v4）下为 NULL，
		// 关联 group 走 subscription_plan_groups 关联表。
		// 互斥约束由 SQL CHECK chk_subscription_plans_mode 保证（migration 151）。
		field.Int64("group_id").
			Optional().
			Nillable(),
		field.Float("wallet_quota_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Optional().
			Nillable().
			Comment("钱包模式月度总额度（USD）。NOT NULL = v4 钱包；NULL = v3 单 group"),
		// plan_type 区分月卡 / 额度卡：
		//   subscription = 月卡，validity_days 控时长（30 天），到期冻结余额
		//   credits      = 额度卡，永久有效（validity_days=36500），烧完为止
		// 取值与互斥约束（credits 必须钱包模式）由 migration 153 保证。
		field.String("plan_type").
			MaxLen(16).
			Default("subscription").
			Comment("subscription = 月卡（validity_days 控时长，到期冻结）；credits = 额度卡（永久有效）"),
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.String("description").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.Float("price").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}),
		field.Float("original_price").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,2)"}).
			Optional().
			Nillable(),
		field.Int("validity_days").
			Default(30),
		field.String("validity_unit").
			MaxLen(10).
			Default("day"),
		field.String("features").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.String("product_name").
			MaxLen(100).
			Default(""),
		field.Bool("for_sale").
			Default(true),
		field.Int("sort_order").
			Default(0),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SubscriptionPlan) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id"),
		index.Fields("for_sale"),
	}
}

func (SubscriptionPlan) Edges() []ent.Edge {
	return []ent.Edge{
		// 钱包模式 plan 关联多个 group（M:N），走 subscription_plan_groups 关联表。
		edge.To("plan_groups", SubscriptionPlanGroup.Type),
	}
}
