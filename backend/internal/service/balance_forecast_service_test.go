package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type forecastUsers struct{ db *sql.DB }

func (u forecastUsers) GetByID(ctx context.Context, id int64) (*User, error) {
	user := &User{ID: id, Username: "Forecast fixture", BalanceNotifyExtraEmails: []NotifyEmailEntry{{Email: "forecast@example.invalid", Verified: true}}}
	err := u.db.QueryRowContext(ctx, `SELECT balance,status,balance_notify_enabled FROM users WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&user.Balance, &user.Status, &user.BalanceNotifyEnabled)
	return user, err
}
func forecastServiceFixture(t *testing.T) (*BalanceForecastService, *notificationEmailMemorySettingRepo, *notificationEmailTestSMTPServer, *sql.DB, *time.Time) {
	t.Helper()
	dsn := os.Getenv("FORECAST_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires task-owned loopback forecast PostgreSQL")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"localhost", "127.0.0.1"}, u.Hostname())
	require.Equal(t, "/balance_forecast_task", u.Path)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("forecast_service_%d", time.Now().UnixNano())
	_, err = db.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	private, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, private.Close())
		_, err := db.Exec("DROP SCHEMA " + schema + " CASCADE")
		require.NoError(t, err)
		require.NoError(t, db.Close())
	})
	projection, err := os.ReadFile("../balanceforecast/testdata/schema.sql")
	require.NoError(t, err)
	_, err = private.Exec(string(projection))
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/332_balance_forecast_delivery.sql")
	require.NoError(t, err)
	_, err = private.Exec(string(migration))
	require.NoError(t, err)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	_, err = private.Exec(`INSERT INTO balance_forecast_coverage VALUES(true,$1);`, now.Add(-48*time.Hour))
	require.NoError(t, err)
	_, err = private.Exec(`INSERT INTO users(id,balance,created_at) VALUES(1,1,$1)`, now.Add(-48*time.Hour))
	require.NoError(t, err)
	for i, h := range []int{3, 2, 1} {
		raw, err := json.Marshal(map[string]any{"command": map[string]any{"UserID": 1, "BalanceCost": 10}})
		require.NoError(t, err)
		_, err = private.Exec(`INSERT INTO usage_billing_events VALUES($1,1,1,$2,'complete','{"Applied":true,"NewBalance":1}',$3)`, fmt.Sprint(i), string(raw), now.Add(-time.Duration(h)*time.Hour))
		require.NoError(t, err)
	}
	smtp := startNotificationEmailTestSMTPServer(t)
	settings := newNotificationEmailMemorySettingRepo()
	require.NoError(t, settings.SetMultiple(context.Background(), smtp.settings()))
	require.NoError(t, settings.SetMultiple(context.Background(), map[string]string{SettingKeyBalanceForecastEnabled: "true", SettingKeyBalanceForecastThresholdHours: "12", SettingKeyBalanceForecastMaxNotifications: "3", SettingKeyBalanceLowNotifyRechargeURL: "https://example.invalid/recharge"}))
	mail := NewNotificationEmailService(settings, NewEmailService(settings, nil))
	svc := NewBalanceForecastService(private, settings, forecastUsers{private}, mail)
	svc.now = func() time.Time { return now }
	return svc, settings, smtp, private, &now
}
func TestBalanceForecastPostgresSMTPAndRestartCap(t *testing.T) {
	svc, settings, smtp, db, now := forecastServiceFixture(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if i > 0 {
			*now = now.Add(time.Hour)
		}
		restarted := NewBalanceForecastService(db, settings, forecastUsers{db}, svc.mail)
		restarted.now = svc.now
		require.NoError(t, restarted.RunOnce(ctx))
	}
	require.EqualValues(t, 3, smtp.messageCount(), "restart cannot reset a continuous episode")
	require.Contains(t, smtp.lastMessageBody(t), "example.invalid/recharge")
	require.Contains(t, smtp.lastMessageBody(t), "subscription spending is excluded")
	var successes int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM balance_forecast_outbox WHERE state='sent'`).Scan(&successes))
	require.Equal(t, 3, successes)
}
func TestBalanceForecastPostgresPreferencesAndDisable(t *testing.T) {
	svc, settings, smtp, db, now := forecastServiceFixture(t)
	ctx := context.Background()
	require.NoError(t, settings.Set(ctx, notificationEmailPreferenceKey(NotificationEmailEventBalanceLow, "forecast@example.invalid"), "unsubscribed"))
	require.NoError(t, svc.RunOnce(ctx))
	require.Zero(t, smtp.messageCount())
	require.NoError(t, settings.Delete(ctx, notificationEmailPreferenceKey(NotificationEmailEventBalanceLow, "forecast@example.invalid")))
	require.NoError(t, settings.Set(ctx, SettingKeyBalanceForecastEnabled, "false"))
	require.NoError(t, svc.RunOnce(ctx))
	require.Zero(t, smtp.messageCount())
	require.NoError(t, settings.Set(ctx, SettingKeyBalanceForecastEnabled, "true"))
	_, err := db.Exec(`UPDATE users SET balance_notify_enabled=false WHERE id=1`)
	require.NoError(t, err)
	require.NoError(t, svc.RunOnce(ctx))
	require.Zero(t, smtp.messageCount())
	_, err = db.Exec(`UPDATE users SET balance_notify_enabled=true WHERE id=1`)
	require.NoError(t, err)
	daily := notificationEmailDeliveryKey(NotificationEmailEventBalanceLow, "balance_low", "1", "forecast@example.invalid", now.UTC().Format("2006-01-02"))
	require.NoError(t, settings.Set(ctx, daily, "sent"))
	require.NoError(t, svc.RunOnce(ctx))
	require.Zero(t, smtp.messageCount())
	require.NoError(t, settings.Delete(ctx, daily))
	require.NoError(t, svc.RunOnce(ctx))
	require.EqualValues(t, 1, smtp.messageCount())
}
func TestBalanceForecastStopJoinsWithoutSending(t *testing.T) {
	svc := &BalanceForecastService{}
	svc.Start()
	svc.Start()
	svc.Stop()
	svc.Stop()
}

func TestBalanceForecastSMTPAcceptanceThenDatabaseFailureDoesNotReplay(t *testing.T) {
	svc, settings, smtp, db, now := forecastServiceFixture(t)
	ctx := context.Background()
	require.NoError(t, settings.Set(ctx, SettingKeyBalanceForecastMaxNotifications, "1"))
	_, err := db.Exec(`CREATE FUNCTION reject_forecast_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture: finish unavailable'; END $$;
 CREATE TRIGGER reject_forecast_finish BEFORE UPDATE ON balance_forecast_outbox FOR EACH ROW EXECUTE FUNCTION reject_forecast_finish()`)
	require.NoError(t, err)
	require.Error(t, svc.RunOnce(ctx))
	require.EqualValues(t, 1, smtp.messageCount())
	_, err = db.Exec(`DROP TRIGGER reject_forecast_finish ON balance_forecast_outbox; DROP FUNCTION reject_forecast_finish()`)
	require.NoError(t, err)
	*now = now.Add(time.Hour)
	restarted := NewBalanceForecastService(db, settings, forecastUsers{db}, svc.mail)
	restarted.now = svc.now
	require.NoError(t, restarted.RunOnce(ctx))
	require.EqualValues(t, 1, smtp.messageCount(), "unknown post-SMTP commit must not replay")
	var state string
	require.NoError(t, db.QueryRow(`SELECT state FROM balance_forecast_outbox`).Scan(&state))
	require.Equal(t, "sending", state)
}
func TestBalanceForecastSMTPFailureConsumesConservativeAllowance(t *testing.T) {
	svc, settings, smtp, db, now := forecastServiceFixture(t)
	ctx := context.Background()
	require.NoError(t, settings.Set(ctx, SettingKeyBalanceForecastMaxNotifications, "1"))
	smtp.close() // Only this fixture's loopback listener; no external SMTP.
	require.NoError(t, svc.RunOnce(ctx))
	var state string
	require.NoError(t, db.QueryRow(`SELECT state FROM balance_forecast_outbox`).Scan(&state))
	require.Equal(t, "uncertain", state)
	*now = now.Add(time.Hour)
	require.NoError(t, svc.RunOnce(ctx))
	require.Zero(t, smtp.messageCount())
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM balance_forecast_outbox`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestBalanceForecastRecoverySurvivesLargeBoundedRotation(t *testing.T) {
	for _, users := range []int{4000, 4001} {
		t.Run(fmt.Sprint(users), func(t *testing.T) {
			svc, settings, smtp, db, now := forecastServiceFixture(t)
			ctx := context.Background()
			// Only user 1 has reliable receipts and a verified recipient; the
			// remaining active users still belong to the bounded scanner page.
			_, err := db.ExecContext(ctx, `INSERT INTO users(id,balance,created_at) SELECT id,30,$2 FROM generate_series(2,$1::bigint) id`, users, now.Add(-48*time.Hour))
			require.NoError(t, err)
			require.NoError(t, settings.Set(ctx, SettingKeyBalanceForecastMaxNotifications, "1"))
			require.NoError(t, svc.RunOnce(ctx))
			require.EqualValues(t, 1, smtp.messageCount())
			_, err = db.ExecContext(ctx, `UPDATE users SET balance=100 WHERE id=1`)
			require.NoError(t, err)
			// Actual RunOnce pagination needs 15 minutes to revisit the first
			// user at these boundaries, including the exact-4000 EOF/reset tick.
			for tick := 0; tick < 15; tick++ {
				*now = now.Add(5 * time.Minute)
				require.NoError(t, svc.RunOnce(ctx))
			}
			var active bool
			require.NoError(t, db.QueryRowContext(ctx, `SELECT active FROM balance_forecast_episodes WHERE user_id=1`).Scan(&active))
			require.False(t, active, "normal full rotation must permit one hour of continuous recovery")
			_, err = db.ExecContext(ctx, `UPDATE users SET balance=1 WHERE id=1`)
			require.NoError(t, err)
			for tick := 0; tick < 3; tick++ {
				*now = now.Add(5 * time.Minute)
				require.NoError(t, svc.RunOnce(ctx))
			}
			require.EqualValues(t, 2, smtp.messageCount(), "recovered user must receive the later warning despite the previous cap")
		})
	}
}
