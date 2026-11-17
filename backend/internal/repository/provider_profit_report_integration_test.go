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
	mainMetrics("2", "45", "45", "55", "0.55")
	check("30", "null", "null", 1)
	b.SettledAmount = &ten
	f.counts(w, 2, 0, a, b, external)
	check("40", "40", "60", 2)
	mainMetrics("2", "45", "45", "55", "0.55")
	newChannel := f.id(`INSERT INTO upstream_channels(name,code) VALUES('New location','new-location') RETURNING id`)
	_, err := f.r.SetProviderProfitLink(context.Background(), f.pid, service.ProviderProfitLinkMutation{Revision: 1, ChannelID: newChannel, Confirm: true})
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
	for k, v := range map[string]string{"bill_requests": "1", "upstream_cost": "0", "gross_profit": "0", "gross_margin": "null"} {
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
	check(rows[key], map[string]string{"assignment": `"provider_url"`, "supplier_requests": "1", "bill_requests": "1", "upstream_cost": "34", "gross_profit": "46", "gross_margin": "0.575"})
	require.NotEqual(t, "null", string(rows[key]["bills_synced_at"]))
	f.exec(`DELETE FROM provider_profit_sync_runs WHERE provider_id=$1`, pid)
	_, rows = read()
	require.NotEqual(t, "null", string(rows[key]["bills_synced_at"]), "saved charges keep a freshness time without a new upstream pull")
	check(rows[key], map[string]string{"bill_requests": "1", "upstream_cost": "34"})
	check(rows[fmt.Sprintf("channel:%d", other)], map[string]string{"supplier_requests": "0", "estimated_requests": "1", "upstream_cost": "6", "gross_profit": "14", "gross_margin": "0.7"})
	check(s, map[string]string{"requests": "2", "request_income": "100", "upstream_cost": "40", "gross_profit": "60", "gross_margin": "0.6"})
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
	for rev, dest := range []int64{channel, f.channel} {
		_, err := f.r.SetProviderProfitLink(context.Background(), f.pid, service.ProviderProfitLinkMutation{Revision: int64(rev + 1), ChannelID: dest, Confirm: true})
		require.NoError(t, err)
		check()
	}
}
