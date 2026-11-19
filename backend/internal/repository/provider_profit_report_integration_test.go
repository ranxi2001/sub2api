//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func (f *profitChargeFixture) report(channel int64, w service.ProviderProfitWindow) map[string]json.RawMessage {
	f.t.Helper()
	s, err := f.r.ReadProfitOperations(context.Background(), service.UpstreamOperationsFilter{ChannelID: channel, From: w.From, Until: w.Until, Model: "fixture-model", ModelScope: "public", Dimension: "upstream", Sort: "name", Direction: "asc", Page: 1, PageSize: 20})
	require.NoError(f.t, err)
	var report struct {
		Summary map[string]json.RawMessage
		Rows    []map[string]json.RawMessage
	}
	require.NoError(f.t, json.Unmarshal(s.Usage, &report))
	var requests int
	require.NoError(f.t, json.Unmarshal(report.Summary["requests"], &requests))
	if requests == 0 {
		require.Empty(f.t, report.Rows)
	} else {
		require.Len(f.t, report.Rows, 1, "the selected channel must have exactly one bucket")
		for _, field := range []string{"request_income", "supplier_requests", "bill_requests", "estimated_requests", "upstream_cost", "gross_profit", "gross_margin", "reported_usage_cost", "reconciled_requests", "covered_reconciled_cost", "reconciled_cost", "reconciled_profit"} {
			require.Contains(f.t, report.Rows[0], field)
			require.Equal(f.t, report.Summary[field], report.Rows[0][field])
		}
	}
	return report.Summary
}

func TestProviderProfitReportPartialThenSettledWithSharedWallet(t *testing.T) {
	f := newProfitChargeFixture(t)
	second := f.imported(f.pid, 12)
	at := f.now()
	w := service.ProviderProfitWindow{From: at, Until: at.Add(time.Microsecond)}
	f.usageWithIncome(f.account, "receipt-a", at, 65)
	f.usageWithIncome(second, "receipt-b", at, 35)
	a := profitRemote(110, 11, "receipt-a", at)
	b := profitRemote(120, 12, "receipt-b", at)
	external := profitRemote(130, 11, "outside-this-installation", at)
	thirty, ten, twenty := "30", "10", "20"
	a.Amount, b.Amount, external.Amount = "32", "13", "20"
	a.SettledAmount, external.SettledAmount = &thirty, &twenty
	f.exec(`UPDATE mature_providers SET metrics='{"today_spend":60,"total_spend":60,"currency":"USD"}',observed_at=clock_timestamp() WHERE id=$1`, f.pid)
	check := func(covered, cost, profit string, n int) {
		t.Helper()
		s := f.report(f.channel, w)
		for k, v := range map[string]string{"requests": "2", "request_income": "100", "reported_usage_requests": "2", "reported_usage_cost": "45", "covered_reconciled_cost": covered, "reconciled_cost": cost, "reconciled_profit": profit} {
			require.JSONEq(t, v, string(s[k]), k)
		}
		var count int
		require.NoError(t, json.Unmarshal(s["reconciled_requests"], &count))
		require.Equal(t, n, count)
		wallet, err := f.r.ListProviderProfitLinks(context.Background(), 1, 10)
		require.NoError(t, err)
		require.Len(t, wallet.Items, 1)
		require.NotNil(t, wallet.Items[0].Metrics.TodaySpend)
		require.Equal(t, 60.0, *wallet.Items[0].Metrics.TodaySpend)
	}
	mainMetrics := func(bills, subtotal, cost, profit, margin string) {
		t.Helper()
		s := f.report(f.channel, w)
		for k, v := range map[string]string{"supplier_requests": "2", "bill_requests": bills, "estimated_requests": "0", "reported_usage_cost": subtotal, "upstream_cost": cost, "gross_profit": profit, "gross_margin": margin} {
			require.JSONEq(t, v, string(s[k]), k)
		}
	}
	mainMetrics("0", "null", "null", "null", "null")
	f.counts(w, 1, 1, a, external)
	mainMetrics("1", "32", "null", "null", "null")
	f.counts(w, 2, 0, a, b, external)
	// Matched bills stay visible as reported_usage_cost. They do not price this window:
	// it is not the channel-operations consumption day, so today's wallet spend is not applied either.
	mainMetrics("2", "45", "null", "null", "null")
	check("30", "null", "null", 1)
	b.SettledAmount = &ten
	f.counts(w, 2, 0, a, b, external)
	check("40", "40", "60", 2)
	mainMetrics("2", "45", "null", "null", "null")
	newChannel := f.id(`INSERT INTO upstream_channels(name,code) VALUES('New location','new-location') RETURNING id`)
	_, err := f.r.SetProviderProfitLink(context.Background(), f.pid, service.ProviderProfitLinkMutation{Revision: f.id(`SELECT revision FROM provider_profit_links WHERE provider_id=$1`, f.pid), ChannelID: newChannel, Confirm: true})
	require.NoError(t, err)
	f.counts(w, 2, 0, a, b, external)
	check("40", "40", "60", 2)
	require.JSONEq(t, "0", string(f.report(newChannel, w)["requests"]))
}

func TestProviderProfitReportZeroBillAndZeroIncome(t *testing.T) {
	f := newProfitChargeFixture(t)
	at := f.now()
	w := service.ProviderProfitWindow{From: at, Until: at.Add(time.Microsecond)}
	f.usageWithIncome(f.account, "free", at, 0)
	bill := profitRemote(410, 11, "free", at)
	bill.Amount = "0"
	f.counts(w, 1, 0, bill)
	s := f.report(f.channel, w)
	for k, v := range map[string]string{"bill_requests": "1", "upstream_cost": "null", "gross_profit": "null", "gross_margin": "null", "cost_source": `"wallet_unavailable"`} {
		require.JSONEq(t, v, string(s[k]), k)
	}
}

// Registration should move the old URL bucket exactly once, without requiring
// a re-import or even a request after registration. An explicit manual channel
// on the very same URL keeps its own estimate, including in the combined total.
func TestProviderProfitReportRegisterSiteAfterUsage(t *testing.T) {
	f := newProfitChargeFixture(t)
	const site = "https://later-supplier.invalid"
	const api = site + "/v1"
	account := func() int64 {
		return f.id(`INSERT INTO accounts(name,type,platform,credentials,extra) VALUES('manual key','apikey','openai',jsonb_build_object('base_url',$1::text,'api_key','fixture-only'),'{"cost_multiplier":0.5}') RETURNING id`, api)
	}
	matched, manual := account(), account()
	other := f.id(`INSERT INTO upstream_channels(name,code) VALUES('Independent manual channel','manual-only') RETURNING id`)
	f.exec(`INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES($1,$2,1)`, manual, other)
	at := f.now()
	w := service.ProviderProfitWindow{From: at, Until: at.Add(time.Microsecond)}
	f.usageWithIncome(matched, "historical-bill", at, 80)
	f.usageWithIncome(manual, nil, at, 20)
	q := service.UpstreamOperationsFilter{From: w.From, Until: w.Until, Dimension: "upstream", ModelScope: "public", Sort: "upstream_cost", Direction: "desc", Page: 1, PageSize: 20}
	read := func() (map[string]json.RawMessage, map[string]map[string]json.RawMessage) {
		t.Helper()
		s, err := f.r.ReadProfitOperations(context.Background(), q)
		require.NoError(t, err)
		var result struct {
			Summary map[string]json.RawMessage
			Rows    []map[string]json.RawMessage
		}
		require.NoError(t, json.Unmarshal(s.Usage, &result))
		rows := make(map[string]map[string]json.RawMessage)
		for _, r := range result.Rows {
			var k string
			require.NoError(t, json.Unmarshal(r["key"], &k))
			require.NotContains(t, rows, k)
			rows[k] = r
		}
		return result.Summary, rows
	}
	check := func(data map[string]json.RawMessage, want map[string]string) {
		t.Helper()
		for k, v := range want {
			require.JSONEq(t, v, string(data[k]), k)
		}
	}
	s, rows := read()
	require.Len(t, rows, 2)
	check(rows[api], map[string]string{"requests": "1", "upstream_cost": "6", "supplier_requests": "0"})
	check(s, map[string]string{"requests": "2", "request_income": "100", "upstream_cost": "12"})
	pid := f.id(`INSERT INTO mature_providers(name,site_type,base_url) VALUES('Later supplier','sub2api',$1) RETURNING id`, site)
	f.exec(`UPDATE provider_profit_links SET cost_mode='wallet',revision=revision+1 WHERE provider_id=$1`, pid)
	channel := f.id(`SELECT channel_id FROM provider_profit_links WHERE provider_id=$1`, pid)
	key := fmt.Sprintf("channel:%d", channel)
	s, rows = read()
	require.Len(t, rows, 2)
	require.NotContains(t, rows, api)
	check(rows[key], map[string]string{"assignment": `"provider_url"`, "provider_id": fmt.Sprint(pid), "requests": "1", "bill_requests": "0", "upstream_cost": "null", "bills_synced_at": "null"})
	check(s, map[string]string{"requests": "2", "request_income": "100", "supplier_requests": "1", "estimated_requests": "1", "upstream_cost": "null", "gross_profit": "null"})
	bill := profitRemote(501, 99, "historical-bill", at)
	bill.Amount, bill.UniqueAtSite = "34", true
	epoch, err := f.r.ProviderProfitIdentityFrom(context.Background(), pid)
	require.NoError(t, err)
	out, err := f.r.ReconcileProviderProfitCharges(context.Background(), pid, epoch, site, w, []service.ProviderProfitRemoteCharge{bill})
	require.NoError(t, err)
	require.Equal(t, 1, out.Matched)
	s, rows = read()
	require.Len(t, rows, 2)
	check(rows[key], map[string]string{"assignment": `"provider_url"`, "supplier_requests": "1", "bill_requests": "1", "upstream_cost": "null", "gross_profit": "null", "gross_margin": "null", "cost_source": `"wallet_unavailable"`})
	require.NotEqual(t, "null", string(rows[key]["bills_synced_at"]))
	f.exec(`DELETE FROM provider_profit_sync_runs WHERE provider_id=$1`, pid)
	_, rows = read()
	require.NotEqual(t, "null", string(rows[key]["bills_synced_at"]), "saved charges keep a freshness time without a new upstream pull")
	check(rows[key], map[string]string{"bill_requests": "1", "upstream_cost": "null"})
	check(rows[fmt.Sprintf("channel:%d", other)], map[string]string{"supplier_requests": "0", "estimated_requests": "1", "upstream_cost": "6", "gross_profit": "14", "gross_margin": "0.7", "cost_source": `"estimate"`})
	check(s, map[string]string{"requests": "2", "request_income": "100", "upstream_cost": "null", "gross_profit": "null", "gross_margin": "null", "cost_source": `"wallet_unavailable"`})
}

func TestProviderProfitIndependentOriginAndHistoricalAliases(t *testing.T) {
	f := newProfitChargeFixture(t)
	const independent = "https://other-wallet.invalid"
	const alias = "https://PROFIT-fixture.invalid.:443/"
	providerAt := func(base string) int64 {
		return f.id(`INSERT INTO mature_providers(name,site_type,base_url) VALUES('other entry','sub2api',$1) RETURNING id`, base)
	}
	p2, p3 := providerAt(independent), providerAt(alias)
	other, same := f.imported(p2, 11), f.imported(p3, 11)
	legacy := f.imported(f.pid, 11)
	f.exec(`UPDATE accounts SET extra=extra #- '{provider_binding,provider_revision}' WHERE id=$1`, legacy)
	at := f.now()
	w := service.ProviderProfitWindow{From: at, Until: at.Add(time.Microsecond)}
	f.usage(f.account, "coincidence", at)
	f.usage(other, "coincidence", at)
	f.usage(f.account, "ambiguous", at)
	f.usage(same, "ambiguous", at)
	// The unversioned import cannot own a wallet at this instant, but its
	// request still makes the verified account's receipt ambiguous.
	require.Zero(t, f.id(`SELECT count(*) FROM provider_profit_account_at($1,$2)`, legacy, at))
	f.usage(f.account, "legacy-collision", at)
	f.usage(legacy, "legacy-collision", at)
	// Changing where the competing catalog entry points today must not make
	// its original usage appear to be from an independent site.
	f.exec(`UPDATE mature_providers SET base_url='https://later.invalid',revision=revision+1 WHERE id=$1`, p3)
	x := profitRemote(200, 11, "coincidence", at)
	x.Amount = "4"
	x.SettledAmount = &x.Amount
	y := profitRemote(201, 11, "ambiguous", at)
	y.Amount = "3"
	y.SettledAmount = &y.Amount
	z := profitRemote(202, 11, "legacy-collision", at)
	z.Amount = "2"
	z.SettledAmount = &z.Amount
	f.counts(w, 1, 2, x, y, z)
	epoch, err := f.r.ProviderProfitIdentityFrom(context.Background(), p2)
	require.NoError(t, err)
	out, err := f.r.ReconcileProviderProfitCharges(context.Background(), p2, epoch, independent, w, []service.ProviderProfitRemoteCharge{x})
	require.NoError(t, err)
	require.Equal(t, service.ProviderProfitSyncResult{Matched: 1}, out)
	require.EqualValues(t, 2, f.id(`SELECT count(DISTINCT origin_hash) FROM provider_profit_charges WHERE upstream_usage_id=200 AND settled_amount=4`))
	require.Zero(t, f.id(`SELECT count(*) FROM provider_profit_charges WHERE upstream_usage_id IN (201,202)`), "neither the alias nor the unversioned import can disambiguate a debit")
}

func TestProviderProfitReportUsesChannelTodaySpend(t *testing.T) {
	f := newProfitChargeFixture(t)
	second := f.imported(f.pid, 12)
	at := f.now()
	start, end := service.ProfitConsumptionDay(at)
	require.False(t, at.Before(start))
	require.True(t, at.Before(end))
	f.usageWithIncome(f.account, "receipt-a", at, 65)
	f.usageWithIncome(second, "receipt-b", at, 35)
	yesterday := start.Add(-12 * time.Hour)
	f.usageWithIncome(f.account, "yesterday-only", yesterday, 80)
	f.exec(`UPDATE mature_providers SET metrics='{"today_spend":60,"total_spend":90,"currency":"USD"}',observed_at=$2 WHERE id=$1`, f.pid, at)
	read := func(q service.UpstreamOperationsFilter) (map[string]json.RawMessage, map[string]map[string]json.RawMessage) {
		t.Helper()
		queryNow := time.Now()
		dayStart, dayEnd := service.ProfitConsumptionDay(queryNow)
		if q.From.Equal(dayStart) && !q.Until.Equal(dayEnd) {
			q.Until = queryNow
		}
		if q.Until.IsZero() {
			q.Until = queryNow
		}
		if !q.Until.After(q.From) {
			q.Until = q.From.Add(time.Millisecond)
		}
		if q.ModelScope == "" {
			q.ModelScope = "public"
		}
		if q.Sort == "" {
			q.Sort = "name"
		}
		if q.Direction == "" {
			q.Direction = "asc"
		}
		if q.Page == 0 {
			q.Page = 1
		}
		if q.PageSize == 0 {
			q.PageSize = 20
		}
		if q.Dimension == "" {
			q.Dimension = "upstream"
		}
		s, err := f.r.ReadProfitOperations(context.Background(), q)
		require.NoError(t, err)
		var result struct {
			Summary map[string]json.RawMessage
			Rows    []map[string]json.RawMessage
		}
		require.NoError(t, json.Unmarshal(s.Usage, &result))
		rows := map[string]map[string]json.RawMessage{}
		for _, row := range result.Rows {
			var key string
			require.NoError(t, json.Unmarshal(row["key"], &key))
			rows[key] = row
		}
		return result.Summary, rows
	}
	check := func(data map[string]json.RawMessage, want map[string]string) {
		t.Helper()
		for k, v := range want {
			require.JSONEq(t, v, string(data[k]), k)
		}
	}
	today := service.UpstreamOperationsFilter{From: start, Until: time.Now()}
	if !today.Until.After(at) {
		today.Until = at.Add(time.Millisecond)
	}
	key := fmt.Sprintf("channel:%d", f.channel)
	s, rows := read(today)
	require.Len(t, rows, 1)
	check(rows[key], map[string]string{"requests": "2", "request_income": "100", "supplier_requests": "2", "bill_requests": "0", "upstream_cost": "60", "gross_profit": "40", "gross_margin": "0.4", "cost_source": `"wallet_today"`, "wallet_currency": `"USD"`})
	check(s, map[string]string{"requests": "2", "request_income": "100", "upstream_cost": "60", "gross_profit": "40", "gross_margin": "0.4", "cost_source": `"wallet_today"`})
	a := profitRemote(110, 11, "receipt-a", at)
	b := profitRemote(120, 12, "receipt-b", at)
	a.Amount, b.Amount = "32", "13"
	f.counts(service.ProviderProfitWindow{From: at, Until: at.Add(time.Microsecond)}, 2, 0, a, b)
	s, rows = read(today)
	check(rows[key], map[string]string{"bill_requests": "2", "reported_usage_cost": "45", "upstream_cost": "60", "gross_profit": "40", "gross_margin": "0.4"})
	check(s, map[string]string{"upstream_cost": "60", "gross_profit": "40"})
	accountQuery := today
	accountQuery.Dimension = "account"
	s, rows = read(accountQuery)
	check(s, map[string]string{"requests": "2", "request_income": "100", "upstream_cost": "60", "gross_profit": "40", "cost_source": `"wallet_today"`})
	require.Len(t, rows, 2)
	for _, row := range rows {
		check(row, map[string]string{"upstream_cost": "null", "gross_profit": "null", "gross_margin": "null", "cost_source": `"wallet_not_split"`})
		require.NotEqual(t, "30", string(row["upstream_cost"]))
		require.NotEqual(t, "60", string(row["upstream_cost"]))
	}
	modelQuery := today
	modelQuery.Model = "fixture-model"
	s, _ = read(modelQuery)
	check(s, map[string]string{"requests": "2", "request_income": "100", "upstream_cost": "null", "gross_profit": "null", "cost_source": `"wallet_not_split"`})
	groupQuery := today
	groupQuery.GroupID = 424242
	s, _ = read(groupQuery)
	check(s, map[string]string{"requests": "0", "upstream_cost": "0"})
	yest := service.UpstreamOperationsFilter{From: start.AddDate(0, 0, -1), Until: start}
	s, rows = read(yest)
	check(s, map[string]string{"requests": "1", "request_income": "80", "upstream_cost": "null", "gross_profit": "null", "cost_source": `"wallet_unavailable"`})
	check(rows[key], map[string]string{"upstream_cost": "null", "cost_source": `"wallet_unavailable"`})
	f.exec(`UPDATE mature_providers SET observed_at=$2 WHERE id=$1`, f.pid, start.Add(-time.Second))
	s, _ = read(today)
	check(s, map[string]string{"upstream_cost": "null", "gross_profit": "null", "cost_source": `"wallet_unavailable"`})
	f.exec(`UPDATE mature_providers SET metrics='{"today_spend":60,"currency":"CNY"}',observed_at=$2 WHERE id=$1`, f.pid, at)
	s, rows = read(today)
	check(s, map[string]string{"upstream_cost": "null", "gross_profit": "null", "cost_source": `"wallet_currency"`})
	check(rows[key], map[string]string{"cost_source": `"wallet_currency"`, "wallet_currency": `"CNY"`, "upstream_cost": "null"})
	f.exec(`UPDATE mature_providers SET metrics='{"today_spend":0,"currency":"USD"}',observed_at=$2,site_type='newapi' WHERE id=$1`, f.pid, at)
	s, rows = read(today)
	check(rows[key], map[string]string{"upstream_cost": "0", "gross_profit": "100", "gross_margin": "1", "cost_source": `"wallet_today"`})
	check(s, map[string]string{"upstream_cost": "0", "gross_profit": "100", "gross_margin": "1"})
	f.exec(`UPDATE mature_providers SET metrics='{"today_spend":60,"currency":"USD"}',observed_at=$2,site_type='sub2api' WHERE id=$1`, f.pid, at)
	manual := f.id(`INSERT INTO accounts(name,type,platform,credentials,extra) VALUES('manual key','apikey','openai',jsonb_build_object('base_url','https://manual.invalid/v1','api_key','fixture-only'),'{"cost_multiplier":0.5}') RETURNING id`)
	other := f.id(`INSERT INTO upstream_channels(name,code) VALUES('Manual only','manual-only') RETURNING id`)
	f.exec(`INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES($1,$2,1)`, manual, other)
	f.usageWithIncome(manual, nil, f.now(), 20)
	snap, err := f.r.ReadProfitOperations(context.Background(), service.UpstreamOperationsFilter{From: start, Until: time.Now(), Dimension: "upstream", ModelScope: "public", Sort: "upstream_cost", Direction: "desc", Page: 1, PageSize: 20})
	require.NoError(t, err)
	var ordered struct {
		Summary map[string]json.RawMessage   `json:"summary"`
		Rows    []map[string]json.RawMessage `json:"rows"`
	}
	require.NoError(t, json.Unmarshal(snap.Usage, &ordered))
	require.Len(t, ordered.Rows, 2)
	var firstKey, secondKey string
	require.NoError(t, json.Unmarshal(ordered.Rows[0]["key"], &firstKey))
	require.NoError(t, json.Unmarshal(ordered.Rows[1]["key"], &secondKey))
	require.Equal(t, key, firstKey)
	require.Equal(t, fmt.Sprintf("channel:%d", other), secondKey)
	check(ordered.Rows[0], map[string]string{"upstream_cost": "60", "cost_source": `"wallet_today"`})
	check(ordered.Rows[1], map[string]string{"supplier_requests": "0", "upstream_cost": "6", "gross_profit": "14", "cost_source": `"estimate"`})
	check(ordered.Summary, map[string]string{"requests": "3", "request_income": "120", "upstream_cost": "66", "gross_profit": "54", "gross_margin": "0.45", "cost_source": `"mixed"`})
}

func TestProviderProfitWindowSnapshotDoesNotReplaceTodaySpend(t *testing.T) {
	f := newProfitChargeFixture(t)
	at := f.now()
	start, _ := service.ProfitConsumptionDay(at)
	yesterdayFrom := start.AddDate(0, 0, -1)
	f.usageWithIncome(f.account, "yesterday-window", yesterdayFrom.Add(time.Hour), 40)
	f.exec(`UPDATE mature_providers SET metrics='{"today_spend":60,"currency":"USD"}',observed_at=$2 WHERE id=$1`, f.pid, at)
	f.exec(`INSERT INTO provider_spend_snapshots(provider_id,window_from,window_until,amount,currency,observed_at,source) VALUES($1,$2,$3,15,'USD',$4,'sub2api_snapshot_v2')`, f.pid, yesterdayFrom, start, at)
	q := service.UpstreamOperationsFilter{From: yesterdayFrom, Until: start, Dimension: "upstream", ModelScope: "public", Sort: "name", Direction: "asc", Page: 1, PageSize: 20}
	s, err := f.r.ReadProfitOperations(context.Background(), q)
	require.NoError(t, err)
	var result struct {
		Summary map[string]json.RawMessage `json:"summary"`
		Rows    []map[string]json.RawMessage
	}
	require.NoError(t, json.Unmarshal(s.Usage, &result))
	require.JSONEq(t, "15", string(result.Summary["upstream_cost"]))
	require.JSONEq(t, `"wallet_window"`, string(result.Summary["cost_source"]))
	require.Len(t, result.Rows, 1)
	require.JSONEq(t, `"wallet_window"`, string(result.Rows[0]["cost_source"]))
	var todaySpend float64
	require.NoError(t, f.db.QueryRow(`SELECT (metrics->>'today_spend')::float8 FROM mature_providers WHERE id=$1`, f.pid).Scan(&todaySpend))
	require.Equal(t, 60.0, todaySpend)
	f.exec(`DELETE FROM provider_spend_snapshots WHERE provider_id=$1`, f.pid)
	s, err = f.r.ReadProfitOperations(context.Background(), q)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(s.Usage, &result))
	require.JSONEq(t, "null", string(result.Summary["upstream_cost"]))
	require.JSONEq(t, `"wallet_unavailable"`, string(result.Summary["cost_source"]))
}

func TestProviderProfitUniqueSiteClaimsAfterUnlinkAndOAuthUsesBinding(t *testing.T) {
	f := newProfitChargeFixture(t)
	const site = profitChargeBase
	urlAccount := f.id(`INSERT INTO accounts(name,type,platform,credentials,extra) VALUES('url key','apikey','openai',jsonb_build_object('base_url',$1::text,'api_key','fixture-only'),'{"cost_multiplier":0.5}') RETURNING id`, site+"/v1")
	other := f.id(`INSERT INTO upstream_channels(name,code) VALUES('Somewhere else','somewhere-else') RETURNING id`)
	f.exec(`INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES($1,$2,1)`, urlAccount, other)
	f.exec(`UPDATE upstream_channel_bindings SET channel_id=NULL, revision=2 WHERE account_id=$1`, urlAccount)
	explicit := f.id(`INSERT INTO accounts(name,type,platform,credentials,extra) VALUES('kept elsewhere','apikey','openai',jsonb_build_object('base_url',$1::text,'api_key','fixture-only'),'{"cost_multiplier":0.5}') RETURNING id`, site+"/v1")
	f.exec(`INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES($1,$2,1)`, explicit, other)
	tenant := f.id(`INSERT INTO accounts(name,type,platform,credentials,extra) VALUES('tenant path','apikey','openai',jsonb_build_object('base_url',$1::text,'api_key','fixture-only'),'{"cost_multiplier":0.5}') RETURNING id`, site+"/tenant/v1")
	oauth := f.id(`INSERT INTO accounts(name,type,platform,credentials,extra) VALUES('dynamic oauth','oauth','gemini','{}'::jsonb,jsonb_build_object('provider_binding',jsonb_build_object('provider_id',$1::bigint,'key_id','oauth-key'))) RETURNING id`, f.pid)
	at := f.now()
	f.usageWithIncome(urlAccount, nil, at, 10)
	f.usageWithIncome(explicit, nil, at, 20)
	f.usageWithIncome(tenant, nil, at, 5)
	f.usageWithIncome(oauth, nil, at, 7)
	q := service.UpstreamOperationsFilter{From: at, Until: at.Add(time.Second), Dimension: "upstream", ModelScope: "public", Sort: "name", Direction: "asc", Page: 1, PageSize: 20}
	s, err := f.r.ReadProfitOperations(context.Background(), q)
	require.NoError(t, err)
	var result struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	require.NoError(t, json.Unmarshal(s.Usage, &result))
	rows := map[string]map[string]json.RawMessage{}
	for _, row := range result.Rows {
		var key string
		require.NoError(t, json.Unmarshal(row["key"], &key))
		rows[key] = row
	}
	providerKey := fmt.Sprintf("channel:%d", f.channel)
	require.Contains(t, rows, providerKey)
	require.JSONEq(t, `"provider_url"`, string(rows[providerKey]["assignment"]))
	require.JSONEq(t, "2", string(rows[providerKey]["requests"]), "unlinked URL and oauth binding share the provider channel")
	require.Contains(t, rows, fmt.Sprintf("channel:%d", other))
	require.JSONEq(t, "1", string(rows[fmt.Sprintf("channel:%d", other)]["requests"]))
	require.NotContains(t, rows, site+"/v1")
	require.Contains(t, rows, site+"/tenant/v1")
	duplicate := f.id(`INSERT INTO mature_providers(name,site_type,base_url) VALUES('same site','sub2api',$1) RETURNING id`, site)
	require.NotZero(t, duplicate)
	s, err = f.r.ReadProfitOperations(context.Background(), q)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(s.Usage, &result))
	rows = map[string]map[string]json.RawMessage{}
	for _, row := range result.Rows {
		var key string
		require.NoError(t, json.Unmarshal(row["key"], &key))
		rows[key] = row
	}
	require.Contains(t, rows, site+"/v1", "two providers sharing a site key must not auto-merge the unlinked URL")
}

func TestProviderProfitLegacyIncomeUnchangedByFirstLink(t *testing.T) {
	f := newProfitChargeFixture(t)
	channel := f.id(`INSERT INTO upstream_channels(name,code) VALUES('Legacy channel','legacy-fixture') RETURNING id`)
	// Recreate exactly the manual row seeded by migration 383. The usage is
	// older than the observed binding and has neither a receipt nor an import.
	f.exec(`INSERT INTO provider_profit_manual_history(account_id,channel_id,revision,effective_from,legacy_at_migration) VALUES($1,$2,1,clock_timestamp(),true)`, f.account, channel)
	at := f.epoch.Add(-time.Hour)
	w := service.ProviderProfitWindow{From: at, Until: at.Add(time.Microsecond)}
	f.usageWithIncome(f.account, nil, at, 17)
	check := func() {
		t.Helper()
		s := f.report(channel, w)
		require.JSONEq(t, "17", string(s["request_income"]))
		require.JSONEq(t, "null", string(s["reconciled_cost"]))
	}
	check()
	for _, dest := range []int64{channel, f.channel} {
		_, err := f.r.SetProviderProfitLink(context.Background(), f.pid, service.ProviderProfitLinkMutation{Revision: f.id(`SELECT revision FROM provider_profit_links WHERE provider_id=$1`, f.pid), ChannelID: dest, Confirm: true})
		require.NoError(t, err)
		check()
	}
}

func TestProviderProfitNamingPreservesEstimateUntilWalletOptIn(t *testing.T) {
	f := newProfitChargeFixture(t)
	// A new URL/name record defaults to estimates, even without credentials.
	pid := f.id(`INSERT INTO mature_providers(name,site_type,base_url) VALUES('new estimate','sub2api','https://estimate.invalid') RETURNING id`)
	var mode string
	require.NoError(t, f.db.QueryRow(`SELECT cost_mode FROM provider_profit_links WHERE provider_id=$1`, pid).Scan(&mode))
	require.Equal(t, "estimate", mode)
	f.exec(`UPDATE provider_profit_links SET cost_mode='estimate',revision=revision+1 WHERE provider_id=$1`, f.pid)
	at := f.now()
	w := service.ProviderProfitWindow{From: at, Until: at.Add(time.Microsecond)}
	f.usageWithIncome(f.account, "estimate-mode", at, 25)
	before := f.report(f.channel, w)
	f.exec(`UPDATE mature_providers SET name='Renamed only' WHERE id=$1`, f.pid)
	f.exec(`UPDATE upstream_channels SET name='Renamed classification' WHERE id=$1`, f.channel)
	after := f.report(f.channel, w)
	ids, err := f.r.ProfitChannelAccountIDs(context.Background(), f.channel)
	require.NoError(t, err)
	require.Contains(t, ids, f.account)
	for _, key := range []string{"requests", "request_income", "upstream_cost", "gross_profit", "cost_source"} {
		require.JSONEq(t, string(before[key]), string(after[key]))
	}
	require.JSONEq(t, `"estimate"`, string(after["cost_source"]))
	var revision int64
	require.NoError(t, f.db.QueryRow(`SELECT revision FROM provider_profit_links WHERE provider_id=$1`, f.pid).Scan(&revision))
	wallet := "wallet"
	_, err = f.r.SetProviderProfitLink(context.Background(), f.pid, service.ProviderProfitLinkMutation{ChannelID: f.channel, Revision: revision, Confirm: true, CostMode: &wallet})
	require.NoError(t, err)
	missing := f.report(f.channel, w)
	require.JSONEq(t, `"wallet_unavailable"`, string(missing["cost_source"]))
	require.JSONEq(t, `null`, string(missing["upstream_cost"]))
	require.JSONEq(t, string(before["requests"]), string(missing["requests"]))
	require.JSONEq(t, string(before["request_income"]), string(missing["request_income"]))
}
