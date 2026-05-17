package schema

import (
	"time"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UserSubscription holds the schema definition for the UserSubscription entity.
type UserSubscription struct {
	ent.Schema
}

func (UserSubscription) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "user_subscriptions"},
	}
}

func (UserSubscription) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (UserSubscription) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		// group_id 钱包模式（v4）下为 NULL；老的单 group 订阅模式（v3）下必填。
		// 互斥约束由 SQL CHECK chk_user_subscriptions_mode 保证（migration 151）。
		field.Int64("group_id").
			Optional().
			Nillable(),

		field.Time("starts_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("status").
			MaxLen(20).
			Default(domain.SubscriptionStatusActive),

		field.Time("daily_window_start").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("weekly_window_start").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("monthly_window_start").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),

		field.Float("daily_usage_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		field.Float("weekly_usage_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),
		field.Float("monthly_usage_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Default(0),

		// 钱包模式（v4）字段；NULL = 走老的单 group 订阅（v3）。详见 migration 151。
		field.Float("wallet_balance_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Optional().
			Nillable().
			Comment("钱包模式当前余额（USD，含倍率扣减）。NULL = 老 group 订阅模式"),
		field.Float("wallet_initial_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Optional().
			Nillable().
			Comment("钱包模式激活时的总额度（用于 UI 进度条）"),
		field.JSON("locked_rates", map[string]float64{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("订阅级锁定倍率：group_id 字符串 -> rate_multiplier；存在时优先于用户专属倍率和 group 默认倍率"),

		field.Int64("assigned_by").
			Optional().
			Nillable(),
		field.Time("assigned_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}

func (UserSubscription) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("subscriptions").
			Field("user_id").
			Unique().
			Required(),
		// group 在钱包模式下可为空（v4），单 group 订阅模式下必填（v3）。
		edge.From("group", Group.Type).
			Ref("subscriptions").
			Field("group_id").
			Unique(),
		edge.From("assigned_by_user", User.Type).
			Ref("assigned_subscriptions").
			Field("assigned_by").
			Unique(),
		edge.To("usage_logs", UsageLog.Type),
		edge.To("wallet_ledger_entries", SubscriptionWalletLedger.Type),
	}
}

func (UserSubscription) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
		index.Fields("group_id"),
		index.Fields("status"),
		index.Fields("expires_at"),
		// 活跃订阅查询复合索引（线上由 SQL 迁移创建部分索引，schema 仅用于模型可读性对齐）
		index.Fields("user_id", "status", "expires_at"),
		index.Fields("assigned_by"),
		// 唯一约束通过部分索引实现（WHERE deleted_at IS NULL），支持软删除后重新订阅
		// 见迁移文件 016_soft_delete_partial_unique_indexes.sql
		index.Fields("user_id", "group_id"),
		index.Fields("deleted_at"),
	}
}
