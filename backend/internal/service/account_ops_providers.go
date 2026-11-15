package service

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"
)

type accountOpsProviderReader interface {
	ReadAccountOpsProviders(context.Context, []int64) ([]MatureProvider, error)
}
type accountOpsProviderContextKey struct{}
type accountOpsProviderSnapshots map[int64]MatureProvider

func accountOpsProviderID(c AccountOpsConfig, accountID int64) int64 {
	for _, g := range c.Groups {
		for _, id := range g.AccountIDs {
			if id == accountID {
				return g.ProviderID
			}
		}
	}
	return 0
}
func (s *AccountOpsService) withAccountOpsProviderSnapshots(ctx context.Context, c AccountOpsConfig) context.Context {
	ids := []int64{}
	seen := map[int64]bool{}
	for _, g := range c.Groups {
		if g.ProviderID > 0 && !seen[g.ProviderID] {
			ids = append(ids, g.ProviderID)
			seen[g.ProviderID] = true
		}
	}
	snapshots := accountOpsProviderSnapshots{}
	if len(ids) > 0 {
		if reader, ok := s.accounts.(accountOpsProviderReader); ok {
			// A failed read is unknown. Never substitute a member key's quota for a
			// missing provider wallet or manufacture a recovered/zero balance.
			if providers, err := reader.ReadAccountOpsProviders(ctx, ids); err == nil {
				for _, p := range providers {
					snapshots[p.ID] = p
				}
			}
		}
	}
	return context.WithValue(ctx, accountOpsProviderContextKey{}, snapshots)
}
func (s *AccountOpsService) opsBalanceAccount(ctx context.Context, a *Account, c AccountOpsConfig, now time.Time) AccountOpsBalanceAccount {
	id := accountOpsProviderID(c, a.ID)
	if id == 0 || a.Type != AccountTypeAPIKey {
		return opsBalanceAccount(a, now)
	}
	item := AccountOpsBalanceAccount{AccountID: a.ID, AccountName: a.Name, Platform: a.Platform, Type: a.Type, ProviderID: id, Unit: "USD", BalanceStatus: "unknown", UsageWindows: []AccountOpsUsageWindow{}}
	snapshots, ok := ctx.Value(accountOpsProviderContextKey{}).(accountOpsProviderSnapshots)
	if !ok {
		ctx = s.withAccountOpsProviderSnapshots(ctx, c)
		snapshots, _ = ctx.Value(accountOpsProviderContextKey{}).(accountOpsProviderSnapshots)
	}
	p, exists := snapshots[id]
	if !exists {
		return item
	}
	item.Unit = p.Metrics.Currency
	item.ReceivedAt = p.ObservedAt
	item.ProbeEnabled = p.RefreshIntervalSeconds > 0
	if p.Status != "connected" {
		item.BalanceStatus = "failed"
		return item
	}
	if p.Metrics.Balance == nil || math.IsNaN(*p.Metrics.Balance) || math.IsInf(*p.Metrics.Balance, 0) || p.ObservedAt == nil || p.Metrics.Currency == "" {
		return item
	}
	ttl := time.Hour
	if p.RefreshIntervalSeconds > 0 {
		ttl = 2 * time.Duration(p.RefreshIntervalSeconds) * time.Second
	}
	if p.ObservedAt.After(now) {
		return item
	}
	if now.Sub(*p.ObservedAt) > ttl {
		item.BalanceStatus = "stale"
		item.LastKnownBalance = p.Metrics.Balance
		return item
	}
	item.BalanceStatus = "ok"
	item.Balance = p.Metrics.Balance
	return item
}
func (s *AccountOpsService) validateAccountOpsProviderGroups(ctx context.Context, c AccountOpsConfig, accounts []Account) error {
	byID := map[int64]Account{}
	for _, a := range accounts {
		byID[a.ID] = a
	}
	for _, g := range c.Groups {
		if g.ProviderID == 0 {
			continue
		}
		for _, id := range g.AccountIDs {
			if a, ok := byID[id]; ok && a.Type != AccountTypeAPIKey {
				return accountOpsConfigValidation("provider balances apply only to API key accounts")
			}
		}
	}
	return nil
}
func importedAccountOpsProvider(a Account) (int64, string) {
	if a.Type != AccountTypeAPIKey {
		return 0, ""
	}
	binding, _ := a.Extra["provider_binding"].(map[string]any)
	id, ok := opsNumber(binding["provider_id"])
	if !ok || id <= 0 || id > 9007199254740991 || math.Trunc(id) != id {
		return 0, ""
	}
	name, _ := binding["provider_name"].(string)
	return int64(id), name
}

// Imported keys already carry #850's provider ID. Materialize one stable
// monitor group per provider, without guessing shared wallets from hostnames.
func reconcileAccountOpsProviderGroups(c *AccountOpsConfig, accounts []Account) bool {
	linked := map[int64]int{}
	known := map[int64]bool{}
	for _, id := range c.UngroupedAccountIDs {
		known[id] = true
	}
	for i, g := range c.Groups {
		for _, id := range g.AccountIDs {
			known[id] = true
		}
		if g.ProviderID > 0 {
			linked[g.ProviderID] = i
		}
	}
	ordered := append([]Account(nil), accounts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	changed := false
	for _, a := range ordered {
		// Existing manual membership (including an explicit unlink) wins.
		if known[a.ID] {
			continue
		}
		providerID, name := importedAccountOpsProvider(a)
		if providerID == 0 {
			continue
		}
		index, exists := linked[providerID]
		if !exists {
			if name == "" {
				name = "Provider #" + strconv.FormatInt(providerID, 10)
			}
			runes := []rune(name)
			if len(runes) > 80 {
				name = string(runes[:80])
			}
			c.Groups = append(c.Groups, AccountOpsGroup{ID: "manual-provider-" + strconv.FormatInt(providerID, 10), Name: name, Provider: "provider", ProviderID: providerID, AccountIDs: []int64{}})
			index = len(c.Groups) - 1
			linked[providerID] = index
			changed = true
		}
		if appendUniqueAccountOpsGroupMember(&c.Groups[index], a.ID) {
			changed = true
		}
		for i := range c.Groups {
			if i == index {
				continue
			}
			next := []int64{}
			for _, id := range c.Groups[i].AccountIDs {
				if id == a.ID {
					changed = true
				} else {
					next = append(next, id)
				}
			}
			c.Groups[i].AccountIDs = next
		}
	}
	return changed
}
