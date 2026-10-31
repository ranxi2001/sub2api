package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Monetary units are 1e-8 USD, matching the precision of the user balance.
type LotteryPool struct{ ent.Schema }

func (LotteryPool) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "lottery_pools"}}
}
func (LotteryPool) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").MaxLen(100), field.String("mode").MaxLen(20),
		field.Int64("threshold_units").Positive(),
		field.Bool("grant_enabled").Default(false), field.Bool("draw_enabled").Default(false),
		field.Bool("rule_locked").Default(false), field.Int("sort_order").Default(0),
		field.Time("activated_at").Optional().Nillable(),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type LotteryPrize struct{ ent.Schema }

func (LotteryPrize) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "lottery_prizes"}}
}
func (LotteryPrize) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("pool_id"), field.String("name").MaxLen(100), field.String("kind").MaxLen(20),
		field.Int64("weight").Positive(), field.Int64("stock").Default(-1), field.Bool("enabled").Default(true),
		field.Int("sort_order").Default(0), field.Int64("amount_units").Default(0),
		field.Int64("plan_id").Default(0), field.String("plan_name").Default(""), field.Int64("group_id").Default(0), field.Int("validity_days").Default(0),
		field.String("text").Default(""),
		field.Time("deleted_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
func (LotteryPrize) Indexes() []ent.Index { return []ent.Index{index.Fields("pool_id")} }

type LotteryAccount struct{ ent.Schema }

func (LotteryAccount) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "lottery_accounts"}}
}
func (LotteryAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("pool_id"), field.Int64("user_id"), field.Int64("total_units").Default(0),
		field.Int64("earned").Default(0), field.Int64("used").Default(0),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
func (LotteryAccount) Indexes() []ent.Index {
	return []ent.Index{index.Fields("pool_id", "user_id").Unique(), index.Fields("user_id")}
}

type LotteryContribution struct{ ent.Schema }

func (LotteryContribution) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "lottery_contributions"}}
}
func (LotteryContribution) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("pool_id"), field.Int64("user_id"), field.String("source_type").MaxLen(20), field.String("source_id").MaxLen(128),
		field.Int64("amount_units"), field.Int64("net_units"),
		field.Time("created_at").Default(time.Now).Immutable(), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
func (LotteryContribution) Indexes() []ent.Index {
	return []ent.Index{index.Fields("pool_id", "source_type", "source_id").Unique(), index.Fields("user_id", "pool_id"), index.Fields("source_type", "source_id")}
}

type LotteryLedger struct{ ent.Schema }

func (LotteryLedger) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "lottery_ledger"}}
}
func (LotteryLedger) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("pool_id"), field.Int64("user_id"), field.String("action").MaxLen(20),
		field.String("source_type").MaxLen(20), field.String("source_id").MaxLen(128),
		field.Int64("amount_units"), field.Int64("ticket_delta"),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}
func (LotteryLedger) Indexes() []ent.Index {
	return []ent.Index{index.Fields("pool_id", "user_id", "created_at"), index.Fields("user_id")}
}

type LotteryDraw struct{ ent.Schema }

func (LotteryDraw) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "lottery_draws"}}
}
func (LotteryDraw) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("pool_id"), field.Int64("user_id"), field.Int64("prize_id"), field.String("request_key").MaxLen(128),
		field.JSON("prize_snapshot", map[string]any{}), field.Time("created_at").Default(time.Now).Immutable(),
	}
}
func (LotteryDraw) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id", "request_key").Unique(), index.Fields("pool_id", "created_at"), index.Fields("user_id", "created_at")}
}
