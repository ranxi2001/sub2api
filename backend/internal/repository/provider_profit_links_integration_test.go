//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// Exercise the actual upgrade, not just an empty already-migrated database.
// A transaction-local scratch schema shadows only the three input tables and
// runs their original DDL; rollback removes the entire fixture.
func TestProviderProfitLinkUpgradeBackfillsOnlyObservedOwnership(t *testing.T) {
	tx, err := integrationDB.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	run := func(q string) { t.Helper(); _, err := tx.Exec(q); require.NoError(t, err) }
	schema := fmt.Sprintf("link_upgrade_%d", time.Now().UnixNano())
	run("CREATE SCHEMA " + schema)
	run("SET LOCAL search_path=" + schema)
	run(`CREATE TABLE accounts(id bigserial PRIMARY KEY, extra jsonb, credentials jsonb, type text, platform text)`)
	migrate := func(name string) {
		t.Helper()
		raw, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		run(string(raw))
	}
	migrate("312_upstream_channel_entities.sql")
	migrate("345_mature_providers.sql")
	migrate("346_mature_providers_credentials.sql")
	run(`INSERT INTO mature_providers(name,site_type,base_url) VALUES ('Fixture','sub2api','https://fixture.invalid'),('Fixture','sub2api','https://fixture.invalid');
 INSERT INTO upstream_channels(code,name) VALUES ('provider-1','Legacy'),('reserved','Fixture (#1-1)');
 INSERT INTO accounts(type,platform,credentials,extra) SELECT 'apikey','openai','{"base_url":"https://fixture.invalid","api_key":"fixture"}',jsonb_build_object('provider_binding',jsonb_build_object('provider_id',v,'key_id','3')) FROM (VALUES ('1'),('1'),('9999999999999999999'),('bad'),('2')) x(v);
 INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES(1,NULL,4),(5,1,2);
 -- Same site, different wallet, before any identity history was recorded.
 UPDATE mature_providers SET account='wallet-b',revision=2 WHERE id=1`)
	var before time.Time
	require.NoError(t, tx.QueryRow(`SELECT clock_timestamp()-interval '1 second'`).Scan(&before))
	migrate("383_provider_profit_channels.sql")
	var code, assignment, key string
	var n int
	require.NoError(t, tx.QueryRow(`SELECT c.code FROM provider_profit_links l JOIN upstream_channels c ON c.id=l.channel_id WHERE l.provider_id=1`).Scan(&code))
	require.Equal(t, "provider-1-2", code, "neither code nor name collision implies ownership")
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM provider_profit_links`).Scan(&n))
	require.Equal(t, 2, n)
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM provider_profit_account_at(2,$1)`, before).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, tx.QueryRow(`SELECT assignment FROM provider_profit_account_at(1,clock_timestamp()) WHERE channel_id IS NULL AND provider_id IS NULL AND key_id IS NULL`).Scan(&assignment))
	require.Equal(t, "manual_unlinked", assignment)
	checkUnowned := func() {
		t.Helper()
		require.NoError(t, tx.QueryRow(`SELECT count(*) FROM provider_profit_account_at(2,clock_timestamp())`).Scan(&n))
		require.Zero(t, n, "same site and key number do not prove wallet identity")
	}
	checkUnowned()
	run(`UPDATE accounts SET extra=jsonb_set(extra,'{provider_binding,provider_revision}','1') WHERE id=2`)
	checkUnowned()
	run(`UPDATE accounts SET extra=jsonb_set(extra,'{provider_binding,provider_revision}','2') WHERE id=2`)
	require.NoError(t, tx.QueryRow(`SELECT key_id,assignment FROM provider_profit_account_at(2,clock_timestamp()) WHERE provider_id=1`).Scan(&key, &assignment))
	require.Equal(t, "3", key)
	require.Equal(t, "provider_import", assignment)
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM provider_profit_account_source_history WHERE account_id IN (3,4)`).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM provider_profit_manual_history WHERE legacy_at_migration`).Scan(&n))
	require.Equal(t, 2, n)
	require.NoError(t, tx.QueryRow(`SELECT assignment FROM provider_profit_account_at(5,clock_timestamp()) WHERE channel_id=1 AND provider_id IS NULL AND key_id IS NULL`).Scan(&assignment))
	require.Equal(t, "explicit", assignment)
	var plan string
	require.NoError(t, tx.QueryRow(`EXPLAIN (FORMAT JSON) SELECT * FROM accounts a LEFT JOIN LATERAL provider_profit_account_at(a.id,statement_timestamp()) h ON true`).Scan(&plan))
	require.NotContains(t, plan, `"Function Name": "provider_profit_account_at"`, "the report lookup must inline, not invoke a function once per usage row")
}

func TestProviderProfitLinkCASAndHistoricalAttribution(t *testing.T) {
	db, _ := UpstreamChannelsTestDatabase(t)
	ctx := context.Background()
	r := &channelRepository{db: db}
	exec := func(q string, args ...any) { t.Helper(); _, err := db.Exec(q, args...); require.NoError(t, err) }
	queryID := func(q string, args ...any) int64 {
		t.Helper()
		var id int64
		require.NoError(t, db.QueryRow(q, args...).Scan(&id))
		return id
	}
	clock := func() time.Time {
		t.Helper()
		var at time.Time
		require.NoError(t, db.QueryRow(`SELECT clock_timestamp()`).Scan(&at))
		return at
	}
	provider := func() int64 {
		return queryID(`INSERT INTO mature_providers(name,site_type,base_url,notes,last_error,metrics,status,observed_at) VALUES('Fixture','sub2api','https://provider.invalid','private_notes','private_observation','{"balance":987654,"today_spend":0,"total_spend":12,"currency":"USD","debug":"private_metadata"}','connected',clock_timestamp()) RETURNING id`)
	}
	pid, pid2 := provider(), provider()
	cid := queryID(`SELECT channel_id FROM provider_profit_links WHERE provider_id=$1`, pid)
	cid2 := queryID(`SELECT channel_id FROM provider_profit_links WHERE provider_id=$1`, pid2)
	legacy := queryID(`INSERT INTO upstream_channels(code,name) VALUES('manual','Existing') RETURNING id`)
	page, err := r.ListProviderProfitLinks(ctx, 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, page.Items, 1)
	v := page.Items[0]
	require.Equal(t, pid, v.ProviderID)
	require.Equal(t, cid, v.ChannelID)
	require.Equal(t, float64(0), *v.Metrics.TodaySpend)
	require.Equal(t, float64(12), *v.Metrics.TotalSpend)
	raw, err := json.Marshal(page)
	require.NoError(t, err)
	for _, bad := range []string{"private_", "987654", "provider.invalid", "credential", "balance"} {
		require.NotContains(t, string(raw), bad)
	}
	page, err = r.ListProviderProfitLinks(ctx, 3, 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Empty(t, page.Items)
	link := func(id, target, rev int64) (*service.ProviderProfitLink, error) {
		return r.SetProviderProfitLink(ctx, id, service.ProviderProfitLinkMutation{ChannelID: target, Revision: rev, Confirm: true})
	}
	_, err = link(pid, cid2, 1)
	require.ErrorIs(t, err, service.ErrProviderProfitLinkConflict)
	_, err = link(pid, 9999999, 1)
	require.ErrorIs(t, err, service.ErrProviderProfitLinkNotFound)
	one := int64(1)
	require.ErrorIs(t, r.DeleteUpstreamChannel(ctx, cid, service.UpstreamChannelMutation{Revision: &one, Confirm: true}), service.ErrUpstreamChannelConflict)
	v1, err := link(pid, cid, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, v1.Revision)
	aid := queryID(`INSERT INTO accounts(name,type,platform,extra,credentials) VALUES('Imported','apikey','openai',jsonb_build_object('provider_binding',jsonb_build_object('provider_id',$1::bigint,'key_id','7','provider_revision',1)),'{"base_url":"https://provider.invalid","api_key":"fixture-key"}') RETURNING id`, pid)
	check := func(id int64, at time.Time, c, p int64, k, a string) {
		t.Helper()
		var cg, pg int64
		var kg, ag string
		err := db.QueryRow(`SELECT COALESCE(channel_id,0),COALESCE(provider_id,0),COALESCE(key_id,''),assignment FROM provider_profit_account_at($1,$2)`, id, at).Scan(&cg, &pg, &kg, &ag)
		if a == "" {
			require.ErrorIs(t, err, sql.ErrNoRows)
			return
		}
		require.NoError(t, err)
		require.Equal(t, []any{c, p, k, a}, []any{cg, pg, kg, ag})
	}
	first := clock()
	check(aid, first, cid, pid, "7", "provider_import")
	v1, err = link(pid, legacy, 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, v1.Revision)
	_, err = link(pid, cid, 1)
	require.ErrorIs(t, err, service.ErrProviderProfitLinkConflict)
	second := clock()
	check(aid, second, legacy, pid, "7", "provider_import")
	check(aid, first, cid, pid, "7", "provider_import")
	exec(`INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES($1,$2,1)`, aid, cid2)
	third := clock()
	check(aid, third, cid2, pid2, "", "explicit")
	exec(`UPDATE upstream_channel_bindings SET channel_id=NULL,revision=2 WHERE account_id=$1`, aid)
	fourth := clock()
	check(aid, fourth, 0, 0, "", "manual_unlinked")
	_, err = link(pid, cid, 2)
	require.NoError(t, err)
	check(aid, clock(), 0, 0, "", "manual_unlinked")
	check(aid, second, legacy, pid, "7", "provider_import")
	exec(`DELETE FROM upstream_channel_bindings WHERE account_id=$1`, aid)
	exec(`INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES($1,$2,1)`, aid, cid)
	check(aid, clock(), cid, pid, "7", "explicit")
	exec(`DELETE FROM upstream_channel_bindings WHERE account_id=$1`, aid)
	exec(`UPDATE mature_providers SET credential_cipher='session_rotation',revision=revision+1 WHERE id=$1`, pid)
	check(aid, clock(), cid, pid, "7", "provider_import")
	rev := queryID(`SELECT revision FROM provider_profit_links WHERE provider_id=$1`, pid)
	require.EqualValues(t, 3, rev)
	// The real admin edit names the identity columns even when unchanged.
	exec(`UPDATE mature_providers SET account=account,credential_cipher='replaced_credential',revision=revision+1 WHERE id=$1`, pid)
	check(aid, clock(), 0, 0, "", "")
	check(aid, first, cid, pid, "7", "provider_import")
	require.EqualValues(t, 4, queryID(`SELECT revision FROM provider_profit_links WHERE provider_id=$1`, pid))
	exec(`UPDATE accounts SET extra=jsonb_set(extra,'{provider_binding,provider_revision}','3') WHERE id=$1`, aid)
	check(aid, clock(), cid, pid, "7", "provider_import")
	exec(`UPDATE accounts SET credentials=jsonb_set(credentials,'{api_key}','"replaced"') WHERE id=$1`, aid)
	check(aid, clock(), 0, 0, "", "")
	exec(`UPDATE accounts SET extra=jsonb_set(extra,'{provider_binding,key_name}','"renamed"') WHERE id=$1`, aid)
	check(aid, clock(), 0, 0, "", "")
	exec(`DELETE FROM mature_providers WHERE id=$1`, pid)
	channelRev := queryID(`SELECT revision FROM upstream_channels WHERE id=$1`, cid)
	require.NoError(t, r.DeleteUpstreamChannel(ctx, cid, service.UpstreamChannelMutation{Revision: &channelRev, Confirm: true}))
	check(aid, first, cid, pid, "7", "provider_import")
	check(aid, third, cid2, pid2, "", "explicit")
	check(aid, fourth, 0, 0, "", "manual_unlinked")
}
