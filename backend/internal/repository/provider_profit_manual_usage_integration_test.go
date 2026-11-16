//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProviderProfitBillForHistoricalHandEnteredAccount(t *testing.T) {
	f := newProfitChargeFixture(t)
	f.exec(`UPDATE mature_providers SET base_url='https://unrelated-fixture.invalid',revision=revision+1 WHERE id=$1`, f.pid)
	account := func(base string) int64 {
		return f.id(`INSERT INTO accounts(name,type,platform,credentials) VALUES('hand-entered','apikey','openai',jsonb_build_object('base_url',$1::text,'api_key','fixture-only')) RETURNING id`, base)
	}
	local := account("https://PROFIT-fixture.invalid.:443/v1/")
	second := account(profitChargeBase)
	separate := account("https://another-site.invalid")
	tenant := account(profitChargeBase + "/tenant/v1")
	beforeCatalog := f.now().Add(-time.Second)
	w := service.ProviderProfitWindow{From: beforeCatalog, Until: beforeCatalog.Add(time.Second)}
	bill := func(id int64, req string) service.ProviderProfitRemoteCharge {
		r := profitRemote(id, 23, req, beforeCatalog)
		r.UniqueAtSite = true
		return r
	}
	a := f.usage(local, "one", beforeCatalog)
	f.usage(separate, "one", beforeCatalog) // a different site cannot compete
	f.usage(tenant, "one", beforeCatalog)   // same host is not enough for a tenant path
	f.usage(local, "two-remote-keys", beforeCatalog)
	f.usage(local, "two-local-rows", beforeCatalog)
	f.usage(second, "two-local-rows", beforeCatalog.Add(-time.Microsecond))
	f.usage(local, nil, beforeCatalog)
	free := f.usage(local, "free", beforeCatalog)
	ambiguous, zero := bill(301, "two-remote-keys"), bill(303, "free")
	ambiguous.UniqueAtSite, zero.Amount = false, "0"
	rows := []service.ProviderProfitRemoteCharge{bill(300, "one"), ambiguous, bill(302, "two-local-rows"), zero, bill(304, "another-installation")}
	// The old requests have already been written when the operator adds the site.
	f.pid = f.provider()
	f.channel = f.id(`SELECT channel_id FROM provider_profit_links WHERE provider_id=$1`, f.pid)
	var err error
	f.epoch, err = f.r.ProviderProfitIdentityFrom(context.Background(), f.pid)
	require.NoError(t, err)
	require.True(t, beforeCatalog.Before(f.epoch))
	f.counts(w, 2, 3, rows...)
	f.counts(w, 2, 3, rows...)
	require.EqualValues(t, 1, f.id(`SELECT count(*) FROM provider_profit_charges WHERE usage_log_id=$1 AND provider_id=$2 AND key_id=23 AND upstream_usage_id=300 AND settled_amount IS NULL`, a, f.pid))
	require.EqualValues(t, 1, f.id(`SELECT count(*) FROM provider_profit_charges WHERE usage_log_id=$1 AND upstream_usage_id=303 AND amount=0`, free))
	require.EqualValues(t, 2, f.id(`SELECT count(*) FROM provider_profit_charges`))
	// A later account change does not let the new wallet claim another old bill.
	f.exec(`UPDATE mature_providers SET account='different-wallet',revision=revision+1 WHERE id=$1`, f.pid)
	f.epoch, err = f.r.ProviderProfitIdentityFrom(context.Background(), f.pid)
	require.NoError(t, err)
	f.usage(local, "later-wallet", beforeCatalog)
	_, err = f.sync(w, bill(305, "later-wallet"))
	require.NoError(t, err)
	require.Zero(t, f.id(`SELECT count(*) FROM provider_profit_charges WHERE upstream_usage_id=305`))
}

func TestProviderProfitBillCannotOverrideManualDecisionOrImport(t *testing.T) {
	f := newProfitChargeFixture(t)
	manual := f.id(`INSERT INTO accounts(name,type,platform,credentials) VALUES('manual','apikey','openai','{"base_url":"https://profit-fixture.invalid/v1","api_key":"fixture"}') RETURNING id`)
	f.exec(`INSERT INTO upstream_channel_bindings(account_id,channel_id,revision) VALUES($1,NULL,1)`, manual)
	at := f.now()
	w := service.ProviderProfitWindow{From: at, Until: at.Add(time.Second)}
	f.usage(f.account, "reuse", at)
	f.usage(manual, "reuse", at) // explicit unlink still competes for a remote charge
	f.usage(manual, "after-unlink", at)
	a, b := profitRemote(310, 11, "reuse", at), profitRemote(311, 11, "after-unlink", at)
	a.UniqueAtSite, b.UniqueAtSite = true, true
	f.counts(w, 0, 1, a, b)
	require.Zero(t, f.id(`SELECT count(*) FROM provider_profit_charges`))
}
