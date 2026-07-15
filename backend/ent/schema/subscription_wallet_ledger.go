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

// SubscriptionWalletLedger 钱包流水追加表（v4）。
//
// 每次 activation/usage/refund/adjustment/expiration 写一行，永不更新永不删除。
// user_subscriptions.wallet_balance_usd 是其聚合的缓存字段，对账以本表为准。
//
// 数据库提交时和对账任务共同校验：
//
//	SUM(ledger.delta_usd) = wallet_balance_usd
//	activation 已包含初始入账；偏差会阻断写入或进入对账告警。
//
// 详细设计：ai-relay-infra/docs/plans/2026-05-10-wallet-mode-design.md §1.2 §5.3
type SubscriptionWalletLedger struct {
	ent.Schema
}

func (SubscriptionWalletLedger) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "subscription_wallet_ledger"},
	}
}

func (SubscriptionWalletLedger) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("subscription_id"),
		field.Float("delta_usd").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Comment("正=入账（激活/退款）, 负=出账（消费）"),
		field.Float("balance_after").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}).
			Comment("此次操作后余额（冗余，便于排查）"),
		field.String("reason").
			MaxLen(32).
			Comment("activation | usage | refund | adjustment | expiration"),
		field.Int64("payment_order_id").
			Optional().
			Nillable().
			Comment("immutable payment source for activation/topup and its refund reversal"),
		field.Int64("usage_log_id").
			Optional().
			Nillable().
			Comment("仅 reason=usage 时填，关联到 usage_logs.id"),
		field.Int64("operator_id").
			Optional().
			Nillable().
			Comment("仅 refund/adjustment 时填，操作员 user_id"),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (SubscriptionWalletLedger) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("subscription", UserSubscription.Type).
			Ref("wallet_ledger_entries").
			Field("subscription_id").
			Unique().
			Required(),
		edge.From("usage_log", UsageLog.Type).
			Ref("wallet_ledger_entries").
			Field("usage_log_id").
			Unique(),
		edge.From("operator", User.Type).
			Ref("wallet_ledger_operations").
			Field("operator_id").
			Unique(),
	}
}

func (SubscriptionWalletLedger) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("subscription_id", "created_at"),
		index.Fields("usage_log_id").
			Unique().
			Annotations(entsql.IndexWhere("usage_log_id IS NOT NULL AND reason = 'usage'")),
		index.Fields("payment_order_id"),
		index.Fields("reason"),
	}
}
