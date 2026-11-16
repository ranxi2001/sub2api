//go:build integration

package repository

import (
	"context"
	"encoding/json"
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
		for _, field := range []string{"request_income", "reported_usage_cost", "reconciled_requests", "covered_reconciled_cost", "reconciled_cost", "reconciled_profit"} {
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
	f.counts(w, 2, 0, a, b, external)
	check("30", "null", "null", 1)
	b.SettledAmount = &ten
	f.counts(w, 2, 0, a, b, external)
	check("40", "40", "60", 2)
	newChannel := f.id(`INSERT INTO upstream_channels(name,code) VALUES('New location','new-location') RETURNING id`)
	_, err := f.r.SetProviderProfitLink(context.Background(), f.pid, service.ProviderProfitLinkMutation{Revision: 1, ChannelID: newChannel, Confirm: true})
	require.NoError(t, err)
	f.counts(w, 2, 0, a, b, external)
	check("40", "40", "60", 2)
	require.JSONEq(t, "0", string(f.report(newChannel, w)["requests"]))
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
