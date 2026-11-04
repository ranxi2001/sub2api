-- Credit is an administrator-authorized receivable, never a paid order.
-- All writers serialize on users FOR UPDATE, including ordinary usage debit.
CREATE TABLE IF NOT EXISTS auto_credit_config (
 user_id BIGINT PRIMARY KEY REFERENCES users(id),
 enabled BOOLEAN NOT NULL DEFAULT false,
 threshold NUMERIC(20,8), amount NUMERIC(20,8), cap NUMERIC(20,8),
 version BIGINT NOT NULL DEFAULT 0,
 debit_epoch BIGINT NOT NULL DEFAULT 0,
 trigger_identity TEXT NOT NULL DEFAULT 'initial-admission',
 CHECK (threshold IS NULL OR threshold BETWEEN 0 AND 1000000),
 CHECK (amount IS NULL OR amount > 0 AND amount <= 1000000),
 CHECK (cap IS NULL OR cap > 0 AND cap <= 1000000),
 CHECK (NOT enabled OR threshold IS NOT NULL AND amount IS NOT NULL AND cap IS NOT NULL AND amount <= cap)
);
CREATE TABLE IF NOT EXISTS auto_credit_ledger (
 id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id),
 epoch BIGINT NOT NULL, source TEXT NOT NULL DEFAULT 'auto_credit' CHECK(source='auto_credit'),
 trigger_identity TEXT NOT NULL,
 amount NUMERIC(20,8) NOT NULL CHECK(amount>0),
 balance_before NUMERIC(20,8) NOT NULL, balance_after NUMERIC(20,8) NOT NULL,
 note TEXT NOT NULL DEFAULT '自动充值需索取费用' CHECK(note='自动充值需索取费用'),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','settled')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), settled_at TIMESTAMPTZ, settled_by BIGINT REFERENCES users(id),
 UNIQUE(user_id,epoch), CHECK(balance_after=balance_before+amount),
 CHECK((status='pending' AND settled_at IS NULL AND settled_by IS NULL) OR (status='settled' AND settled_at IS NOT NULL AND settled_by IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS auto_credit_user_status ON auto_credit_ledger(user_id,status,id DESC);
CREATE TABLE IF NOT EXISTS auto_credit_audit (
 id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id),
 actor_id BIGINT REFERENCES users(id), ledger_id BIGINT REFERENCES auto_credit_ledger(id),
 action TEXT NOT NULL CHECK(action IN ('configure','issue','settle')),
 detail JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
