//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional/slots"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func slotLeaseFixture(t *testing.T) (*slots.Primary, *slots.Local, slots.Grant) {
	t.Helper()
	user := regionalLedgerUser(t)
	_, err := integrationDB.Exec(`UPDATE users SET concurrency=4 WHERE id=$1`, user)
	require.NoError(t, err)
	r := slots.Resource{Kind: "user", ID: user}
	n := time.Now().UTC().Add(-time.Millisecond).Truncate(time.Microsecond)
	grant := slots.Grant{LeaseID: fmt.Sprintf("slots-%d", user), Region: "us", Resource: r, Slots: 2, Revision: 1, IssuedAt: n, ExpiresAt: n.Add(time.Hour)}
	p := NewRegionalSlotAuthority(integrationDB)
	l := &slots.Local{DB: integrationDB, Region: "us"}
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM regional_local_slots WHERE lease_id IN (SELECT lease_id FROM regional_local_slot_grants WHERE kind=$1 AND resource_id=$2)`, r.Kind, r.ID)
		_, _ = integrationDB.Exec(`DELETE FROM regional_local_slot_grants WHERE kind=$1 AND resource_id=$2`, r.Kind, r.ID)
		_, _ = integrationDB.Exec(`DELETE FROM regional_primary_slots WHERE kind=$1 AND resource_id=$2`, r.Kind, r.ID)
		_, _ = integrationDB.Exec(`DELETE FROM regional_slot_grants WHERE kind=$1 AND resource_id=$2`, r.Kind, r.ID)
		_, _ = integrationDB.Exec(`DELETE FROM regional_slot_budgets WHERE kind=$1 AND resource_id=$2`, r.Kind, r.ID)
	})
	return p, l, grant
}
func TestRegionalSlotLeaseSharedPoolPrimaryAndUSCannotOversubscribe(t *testing.T) {
	ctx := context.Background()
	p, l, g := slotLeaseFixture(t)
	require.NoError(t, p.Issue(ctx, g))
	require.NoError(t, p.Issue(ctx, g))
	require.NoError(t, l.Install(ctx, g))
	type result struct {
		id  string
		ok  bool
		err error
	}
	primary := make(chan result, 12)
	local := make(chan result, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("primary-%d", i)
			ok, err := p.Acquire(ctx, g.Resource, id)
			primary <- result{id, ok, err}
		}(i)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("us-%d", i)
			ok, err := l.Acquire(ctx, g.LeaseID, id)
			local <- result{id, ok, err}
		}(i)
	}
	wg.Wait()
	close(primary)
	close(local)
	pn, ln := 0, 0
	for r := range primary {
		require.NoError(t, r.err)
		if r.ok {
			pn++
		}
	}
	localIDs := []string{}
	for r := range local {
		require.NoError(t, r.err)
		if r.ok {
			ln++
			localIDs = append(localIDs, r.id)
		}
	}
	require.Equal(t, 2, pn)
	require.Equal(t, 2, ln)
	// Restore the process: occupied regional slots remain occupied.
	l = &slots.Local{DB: integrationDB, Region: "us"}
	ok, err := l.Acquire(ctx, g.LeaseID, "post-crash")
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, p.BeginDrain(ctx, "us", g.LeaseID))
	require.NoError(t, l.Drain(ctx, g.LeaseID))
	_, err = l.Close(ctx, g.LeaseID)
	require.ErrorIs(t, err, slots.ErrUnavailable)
	for _, id := range localIDs {
		require.NoError(t, l.Release(ctx, g.LeaseID, id))
		require.NoError(t, l.Release(ctx, g.LeaseID, id))
	}
	receipt, err := l.Close(ctx, g.LeaseID)
	require.NoError(t, err)
	require.ErrorIs(t, p.Close(ctx, "eu", receipt), slots.ErrConflict)
	require.NoError(t, p.Close(ctx, "us", receipt))
	require.NoError(t, p.Close(ctx, "us", receipt))
	ok, err = p.Acquire(ctx, g.Resource, "primary-after-drain")
	require.NoError(t, err)
	require.True(t, ok)
	// An old authenticated token cannot resurrect the closed local grant.
	require.NoError(t, l.Install(ctx, g))
	_, err = l.Acquire(ctx, g.LeaseID, "old-token")
	require.ErrorIs(t, err, slots.ErrUnavailable)
}
func TestRegionalSlotLeaseRenewalConflictAndExpiry(t *testing.T) {
	ctx := context.Background()
	p, l, g := slotLeaseFixture(t)
	g.ExpiresAt = time.Now().UTC().Add(10 * time.Second).Truncate(time.Microsecond)
	require.NoError(t, p.Issue(ctx, g))
	require.NoError(t, l.Install(ctx, g))
	conflict := g
	conflict.Slots++
	require.ErrorIs(t, p.Issue(ctx, conflict), slots.ErrConflict)
	require.ErrorIs(t, l.Install(ctx, conflict), slots.ErrConflict)
	renewed := g
	renewed.Revision++
	renewed.ExpiresAt = g.ExpiresAt.Add(time.Minute)
	require.NoError(t, p.Renew(ctx, renewed))
	require.NoError(t, p.Renew(ctx, renewed))
	require.NoError(t, l.Install(ctx, renewed))
	require.ErrorIs(t, l.Install(ctx, g), slots.ErrConflict)
	// No duplicate global capacity was created by renewal.
	var held int
	require.NoError(t, integrationDB.QueryRow(`SELECT granted FROM regional_slot_budgets WHERE kind=$1 AND resource_id=$2`, g.Resource.Kind, g.Resource.ID).Scan(&held))
	require.Equal(t, 2, held)
	// A different region cannot use an otherwise valid grant.
	require.ErrorIs(t, (&slots.Local{DB: integrationDB, Region: "eu"}).Install(ctx, renewed), slots.ErrConflict)
	require.NoError(t, p.BeginDrain(ctx, "us", g.LeaseID))
	next := renewed
	next.Revision++
	next.ExpiresAt = next.ExpiresAt.Add(time.Minute)
	require.ErrorIs(t, p.Renew(ctx, next), slots.ErrConflict)
	expired := renewed
	expired.LeaseID += "-expired"
	expired.Revision = 1
	expired.IssuedAt = time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	expired.ExpiresAt = time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	require.NoError(t, l.Install(ctx, expired))
	_, err := l.Acquire(ctx, expired.LeaseID, "expired")
	require.ErrorIs(t, err, slots.ErrUnavailable)
	// A draining/expired grant remains deducted from primary capacity until its
	// exact closed receipt arrives; no TTL-based reuse by the primary.
	ok, err := p.Acquire(ctx, g.Resource, "main-1")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = p.Acquire(ctx, g.Resource, "main-2")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = p.Acquire(ctx, g.Resource, "main-3")
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, p.Release(ctx, g.Resource, "main-1"))
	require.NoError(t, p.Release(ctx, g.Resource, "main-1"))
	ok, err = p.Acquire(ctx, g.Resource, "main-1")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRegionalSlotLeaseReducedAuthorityLimitStopsRenewal(t *testing.T) {
	ctx := context.Background()
	p, l, g := slotLeaseFixture(t)
	require.NoError(t, p.Issue(ctx, g))
	require.NoError(t, l.Install(ctx, g))
	_, err := integrationDB.Exec(`UPDATE users SET concurrency=1 WHERE id=$1`, g.Resource.ID)
	require.NoError(t, err)
	renewed := g
	renewed.Revision++
	renewed.ExpiresAt = renewed.ExpiresAt.Add(time.Minute)
	require.ErrorIs(t, p.Renew(ctx, renewed), slots.ErrUnavailable)
	ok, err := p.Acquire(ctx, g.Resource, "primary-reduced")
	require.NoError(t, err)
	require.False(t, ok)
	// Reduction does not confiscate occupied regional grants or invent free
	// capacity. Existing tokens have bounded expiry; their drain is explicit.
	var held int
	require.NoError(t, integrationDB.QueryRow(`SELECT granted FROM regional_slot_budgets WHERE kind=$1 AND resource_id=$2`, g.Resource.Kind, g.Resource.ID).Scan(&held))
	require.Equal(t, 2, held)
}

func TestRegionalSlotLeaseConcurrencyCacheAdapter(t *testing.T) {
	ctx := context.Background()
	p, l, g := slotLeaseFixture(t)
	require.NoError(t, p.Issue(ctx, g))
	require.NoError(t, l.Install(ctx, g))
	rdb := testRedis(t)
	primary, err := NewRegionalConcurrencyCache(NewConcurrencyCache(rdb, 15, 30), p, nil)
	require.NoError(t, err)
	local, err := NewRegionalConcurrencyCache(NewConcurrencyCache(rdb, 15, 30), nil, l)
	require.NoError(t, err)
	// The caller's stale or inflated cap cannot override the authority or a
	// regional grant. Both adapters use their own persistent budget.
	for i := 0; i < 3; i++ {
		ok, err := primary.AcquireUserSlot(ctx, g.Resource.ID, 100, fmt.Sprintf("adapter-primary-%d", i))
		require.NoError(t, err)
		require.Equal(t, i < 2, ok)
		ok, err = local.AcquireUserSlot(ctx, g.Resource.ID, 100, fmt.Sprintf("adapter-us-%d", i))
		require.NoError(t, err)
		require.Equal(t, i < 2, ok)
	}
	count, err := primary.GetUserConcurrency(ctx, g.Resource.ID)
	require.NoError(t, err)
	require.Equal(t, 4, count)
	count, err = local.GetUserConcurrency(ctx, g.Resource.ID)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, local.ReleaseUserSlot(ctx, g.Resource.ID, "adapter-us-0"))
	count, err = local.GetUserConcurrency(ctx, g.Resource.ID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NoError(t, primary.ReleaseUserSlot(ctx, g.Resource.ID, "adapter-primary-0"))
	count, err = primary.GetUserConcurrency(ctx, g.Resource.ID)
	require.NoError(t, err)
	require.Equal(t, 3, count)
	// Unlimited statistics stay in the auxiliary Redis set; releasing them must
	// not release an unrelated persistent regional reservation.
	statsSlot, err := service.NewConcurrencyService(local).AcquireUserSlot(ctx, g.Resource.ID, 0)
	require.NoError(t, err)
	require.True(t, statsSlot.Acquired)
	count, err = primary.GetUserConcurrency(ctx, g.Resource.ID)
	require.NoError(t, err)
	require.Equal(t, 4, count)
	statsSlot.ReleaseFunc()
	count, err = primary.GetUserConcurrency(ctx, g.Resource.ID)
	require.NoError(t, err)
	require.Equal(t, 3, count)
	// Optional legacy acquisition implementations must not leak through the
	// embedded auxiliary cache and bypass SQL. FIFO/Live now use SQL transactions.
	_, fifo := local.(service.APIKeySlotQueueCache)
	require.True(t, fifo)
	_, live := local.(service.LiveConcurrencyCache)
	require.True(t, live)
	_, keys := local.(service.APIKeySlotLeaseCache)
	require.True(t, keys)
	_, err = NewRegionalConcurrencyCache(NewConcurrencyCache(rdb, 15, 30), p, l)
	require.Error(t, err)
}
