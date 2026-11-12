//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPaymentProviderFeeRatesMigrationPreservesHistoricalAndExplicitValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	image := os.Getenv("SUB2API_TEST_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	pg, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("provider_fee_test"),
		tcpostgres.WithUsername("fixture"), tcpostgres.WithPassword("synthetic"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(context.Background())) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	base, err := FS.ReadFile("096_payment_provider_instances.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(base))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO payment_provider_instances (provider_key, name, config) VALUES ('easypay', 'legacy', '{}')`)
	require.NoError(t, err)
	migration, err := FS.ReadFile("342_payment_provider_fee_rates.sql")
	require.NoError(t, err)

	// The ordinary migration runner wraps this DDL in a transaction. A failed
	// migration can roll back without changing the historical provider row.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	var rate sql.NullFloat64
	var methodFees, name, config string
	err = tx.QueryRowContext(ctx, `SELECT fee_rate, method_fee_rates::text, name, config FROM payment_provider_instances WHERE name = 'legacy'`).Scan(&rate, &methodFees, &name, &config)
	require.NoError(t, err)
	require.False(t, rate.Valid)
	require.JSONEq(t, `{}`, methodFees)
	require.Equal(t, "legacy", name)
	require.Equal(t, "{}", config)
	require.NoError(t, tx.Rollback())
	var columnCount int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'payment_provider_instances' AND column_name IN ('fee_rate', 'method_fee_rates')`).Scan(&columnCount)
	require.NoError(t, err)
	require.Zero(t, columnCount)

	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE payment_provider_instances SET fee_rate = 0, method_fee_rates = '{"alipay":1.25,"wxpay":0}' WHERE name = 'legacy'`)
	require.NoError(t, err)
	// Replaying the migration must retain explicit zero and per-method rates.
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `SELECT fee_rate, method_fee_rates::text FROM payment_provider_instances WHERE name = 'legacy'`).Scan(&rate, &methodFees)
	require.NoError(t, err)
	require.True(t, rate.Valid)
	require.Zero(t, rate.Float64)
	require.JSONEq(t, `{"alipay":1.25,"wxpay":0}`, methodFees)
	_, err = db.ExecContext(ctx, `INSERT INTO payment_provider_instances (provider_key, name, config) VALUES ('easypay', 'new-default', '{}')`)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `SELECT fee_rate, method_fee_rates::text FROM payment_provider_instances WHERE name = 'new-default'`).Scan(&rate, &methodFees)
	require.NoError(t, err)
	require.False(t, rate.Valid)
	require.JSONEq(t, `{}`, methodFees)
}
