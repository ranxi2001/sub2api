package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"strings"
)

func manualAccountOpsGroupID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "manual-" + hex.EncodeToString(sum[:])[:32]
}

func legacyManualAccountOpsAccountGroupID(accountID int64) string {
	return manualAccountOpsGroupID("account:" + strconv.FormatInt(accountID, 10))
}

func legacyAccountOpsGroupID(id string) bool {
	return strings.HasPrefix(id, "auto:") || strings.HasPrefix(id, "account:")
}

func cloneAccountOpsGroups(groups []AccountOpsGroup) []AccountOpsGroup {
	cloned := make([]AccountOpsGroup, len(groups))
	for i, group := range groups {
		cloned[i] = group
		cloned[i].AccountIDs = append([]int64(nil), group.AccountIDs...)
	}
	return cloned
}

func hasAccountOpsGroupMember(groups []AccountOpsGroup, accountID int64) bool {
	for _, group := range groups {
		for _, id := range group.AccountIDs {
			if id == accountID {
				return true
			}
		}
	}
	return false
}

func accountOpsGroupMemberIndex(groups []AccountOpsGroup, id string) int {
	for i := range groups {
		if groups[i].ID == id {
			return i
		}
	}
	return -1
}

func accountOpsGroupSite(group AccountOpsGroup, accounts map[int64]*Account) (provider, site string) {
	provider = strings.TrimSpace(group.Provider)
	site = canonicalAccountOpsSite(group.Site)
	for _, id := range group.AccountIDs {
		if account := accounts[id]; account != nil {
			accountProvider, accountSite, _ := accountOpsUpstreamIdentity(account)
			if provider == "" {
				provider = accountProvider
			}
			if site == "" {
				site = accountSite
			}
			break
		}
	}
	if provider == "" {
		provider = "sub2api"
	}
	return provider, site
}

func appendUniqueAccountOpsGroupMember(group *AccountOpsGroup, accountID int64) bool {
	for _, id := range group.AccountIDs {
		if id == accountID {
			return false
		}
	}
	group.AccountIDs = append(group.AccountIDs, accountID)
	return true
}

func copyBalanceRuleForAccount(rule AccountOpsBalanceRule, accountID int64) AccountOpsBalanceRule {
	rule.AccountID = accountID
	if rule.NotifyAlert != nil {
		value := *rule.NotifyAlert
		rule.NotifyAlert = &value
	}
	if rule.NotifyRecovery != nil {
		value := *rule.NotifyRecovery
		rule.NotifyRecovery = &value
	}
	return rule
}

func copyQuotaRuleForAccount(rule AccountOpsQuotaRule, accountID int64) AccountOpsQuotaRule {
	rule.AccountID = accountID
	if rule.NotifyAlert != nil {
		value := *rule.NotifyAlert
		rule.NotifyAlert = &value
	}
	if rule.NotifyRecovery != nil {
		value := *rule.NotifyRecovery
		rule.NotifyRecovery = &value
	}
	return rule
}

// normalizeAccountOpsGroupRules makes the first configured rule in each group
// authoritative for every member of the same account type. The existing
// account-scoped storage remains compatible, while the UI and future members
// observe group-scoped behavior.
func normalizeAccountOpsGroupRules(c *AccountOpsConfig, accounts map[int64]*Account) bool {
	if c == nil {
		return false
	}
	balanceByID := make(map[int64]AccountOpsBalanceRule, len(c.BalanceThresholds))
	for _, rule := range c.BalanceThresholds {
		balanceByID[rule.AccountID] = rule
	}
	quotaByID := make(map[int64]AccountOpsQuotaRule, len(c.QuotaThresholds))
	for _, rule := range c.QuotaThresholds {
		quotaByID[rule.AccountID] = rule
	}
	assigned := make(map[int64]bool)
	newBalance := make([]AccountOpsBalanceRule, 0, len(c.BalanceThresholds))
	newQuota := make([]AccountOpsQuotaRule, 0, len(c.QuotaThresholds))
	changed := false
	for _, group := range c.Groups {
		var balanceSource *AccountOpsBalanceRule
		var quotaSource *AccountOpsQuotaRule
		for _, id := range group.AccountIDs {
			account := accounts[id]
			if account == nil {
				continue
			}
			if account.Type == AccountTypeAPIKey && balanceSource == nil {
				if rule, ok := balanceByID[id]; ok {
					copy := rule
					balanceSource = &copy
				}
			}
			if account.Type == AccountTypeOAuth && quotaSource == nil {
				if rule, ok := quotaByID[id]; ok {
					copy := rule
					quotaSource = &copy
				}
			}
		}
		for _, id := range group.AccountIDs {
			account := accounts[id]
			if account == nil {
				continue
			}
			assigned[id] = true
			switch account.Type {
			case AccountTypeAPIKey:
				if balanceSource != nil {
					newBalance = append(newBalance, copyBalanceRuleForAccount(*balanceSource, id))
				}
			case AccountTypeOAuth:
				if quotaSource != nil {
					newQuota = append(newQuota, copyQuotaRuleForAccount(*quotaSource, id))
				}
			}
		}
	}
	// Rules of ungrouped accounts no longer participate in the group-only
	// threshold page or scanner, so drop them instead of exposing orphan rows.
	if !reflect.DeepEqual(c.BalanceThresholds, newBalance) || !reflect.DeepEqual(c.QuotaThresholds, newQuota) {
		changed = true
		c.BalanceThresholds = newBalance
		c.QuotaThresholds = newQuota
	}
	return changed
}

// reconcileAccountOpsGroups performs the one-time automatic-to-manual
// conversion and then assigns later accounts to the first group for their
// canonical site. It returns whether the persisted config changed.
func (s *AccountOpsService) reconcileAccountOpsGroups(ctx context.Context, c *AccountOpsConfig, accounts []Account) (bool, error) {
	if c == nil {
		return false, nil
	}
	byID := make(map[int64]*Account, len(accounts))
	for i := range accounts {
		byID[accounts[i].ID] = &accounts[i]
	}
	original := cloneAccountOpsGroups(c.Groups)
	wasInitialized := c.GroupsInitialized
	bootstrap := !wasInitialized
	for _, group := range original {
		if legacyAccountOpsGroupID(group.ID) {
			bootstrap = true
			break
		}
	}
	groups := make([]AccountOpsGroup, 0, len(original)+len(accounts))
	legacyTargets := make(map[string]int)
	discoveredTargets := make(map[string]int)
	ungrouped := make(map[int64]bool, len(c.UngroupedAccountIDs))
	for _, id := range c.UngroupedAccountIDs {
		ungrouped[id] = true
	}
	knownAccounts := make(map[int64]bool)
	for _, group := range original {
		for _, id := range group.AccountIDs {
			knownAccounts[id] = true
		}
		if legacyAccountOpsGroupID(group.ID) {
			converted := group
			converted.ID = manualAccountOpsGroupID(group.ID)
			legacyTargets[group.ID] = len(groups)
			groups = append(groups, converted)
			continue
		}
		groups = append(groups, group)
	}
	for id := range ungrouped {
		knownAccounts[id] = true
	}
	for _, rule := range c.BalanceThresholds {
		knownAccounts[rule.AccountID] = true
	}
	for _, rule := range c.QuotaThresholds {
		knownAccounts[rule.AccountID] = true
	}
	changed := !reflect.DeepEqual(original, groups)

	for i := range groups {
		provider, site := accountOpsGroupSite(groups[i], byID)
		if groups[i].Provider != provider || canonicalAccountOpsSite(groups[i].Site) != site {
			groups[i].Provider = provider
			groups[i].Site = site
			changed = true
		}
	}

	// Older automatic grouping could materialize one manual group per account
	// before a New API binding was available. Once the binding exists, resolve
	// the account's real upstream identity and move it to the canonical group.
	// This also removes the now-empty legacy account groups, while retaining
	// user-created empty groups with other IDs.
	legacyGroupsToRemove := make(map[string]bool)
	for i := range accounts {
		account := &accounts[i]
		desired := s.legacyAccountOpsGroupAssignment(ctx, account, AccountOpsConfig{})
		legacyID := legacyManualAccountOpsAccountGroupID(account.ID)
		if desired.ID == "account:"+strconv.FormatInt(account.ID, 10) {
			continue
		}
		legacyIndex := accountOpsGroupMemberIndex(groups, legacyID)
		if legacyIndex < 0 {
			continue
		}
		group := &groups[legacyIndex]
		remaining := group.AccountIDs[:0]
		for _, memberID := range group.AccountIDs {
			if memberID == account.ID {
				changed = true
				continue
			}
			remaining = append(remaining, memberID)
		}
		group.AccountIDs = remaining
		legacyGroupsToRemove[legacyID] = true

		targetID := manualAccountOpsGroupID(desired.ID)
		targetIndex := accountOpsGroupMemberIndex(groups, targetID)
		if targetIndex < 0 {
			groups = append(groups, AccountOpsGroup{ID: targetID, Name: desired.Name, Provider: desired.Provider, Site: desired.Site})
			targetIndex = len(groups) - 1
			changed = true
		}
		target := &groups[targetIndex]
		if target.Provider != desired.Provider || canonicalAccountOpsSite(target.Site) != canonicalAccountOpsSite(desired.Site) {
			target.Provider = desired.Provider
			target.Site = canonicalAccountOpsSite(desired.Site)
			changed = true
		}
		if target.Name == "" {
			target.Name = desired.Name
			changed = true
		}
		if appendUniqueAccountOpsGroupMember(target, account.ID) {
			changed = true
		}
	}
	if len(legacyGroupsToRemove) > 0 {
		kept := groups[:0]
		for _, group := range groups {
			if legacyGroupsToRemove[group.ID] && len(group.AccountIDs) == 0 {
				changed = true
				continue
			}
			kept = append(kept, group)
		}
		groups = kept
	}

	for i := range accounts {
		account := &accounts[i]
		if hasAccountOpsGroupMember(groups, account.ID) {
			delete(ungrouped, account.ID)
			continue
		}
		if !bootstrap && knownAccounts[account.ID] {
			ungrouped[account.ID] = true
			continue
		}
		assignment := s.legacyAccountOpsGroupAssignment(ctx, account, AccountOpsConfig{Groups: original})
		if !bootstrap {
			// Once the initial conversion is complete, a new account inherits the
			// first existing group for its site, regardless of its provider user ID.
			provider, site, _ := accountOpsUpstreamIdentity(account)
			if assignment.Site != "" {
				provider, site = assignment.Provider, assignment.Site
			}
			for groupIndex := range groups {
				if canonicalAccountOpsSite(groups[groupIndex].Site) == canonicalAccountOpsSite(site) && site != "" {
					if appendUniqueAccountOpsGroupMember(&groups[groupIndex], account.ID) {
						changed = true
					}
					assignment = AccountOpsGroupAssignment{ID: groups[groupIndex].ID, Name: groups[groupIndex].Name, Provider: provider, Site: site, Mode: "manual"}
					break
				}
			}
			if hasAccountOpsGroupMember(groups, account.ID) {
				continue
			}
		}
		if assignment.ID == "" {
			continue
		}
		var groupIndex int
		if index, ok := legacyTargets[assignment.ID]; ok {
			groupIndex = index
		} else if index, ok := discoveredTargets[assignment.ID]; ok {
			groupIndex = index
		} else {
			groupIndex = accountOpsGroupMemberIndex(groups, assignment.ID)
		}
		if groupIndex < 0 {
			group := AccountOpsGroup{ID: manualAccountOpsGroupID(assignment.ID), Name: assignment.Name, Provider: assignment.Provider, Site: assignment.Site}
			groups = append(groups, group)
			groupIndex = len(groups) - 1
			discoveredTargets[assignment.ID] = groupIndex
			changed = true
		}
		if appendUniqueAccountOpsGroupMember(&groups[groupIndex], account.ID) {
			delete(ungrouped, account.ID)
			changed = true
		}
	}

	for i := range groups {
		if groups[i].Name == "" {
			groups[i].Name = groups[i].Site
			changed = true
		}
		if groups[i].ID == "" {
			groups[i].ID = manualAccountOpsGroupID(strconv.Itoa(i) + ":" + groups[i].Site)
			changed = true
		}
	}
	c.Groups = groups
	c.GroupsInitialized = true
	newUngrouped := make([]int64, 0, len(ungrouped))
	for _, account := range accounts {
		if ungrouped[account.ID] {
			newUngrouped = append(newUngrouped, account.ID)
		}
	}
	if !reflect.DeepEqual(c.UngroupedAccountIDs, newUngrouped) {
		c.UngroupedAccountIDs = newUngrouped
		changed = true
	}
	if !wasInitialized {
		changed = true
	}
	if normalizeAccountOpsGroupRules(c, byID) {
		changed = true
	}
	return changed, nil
}

func (s *AccountOpsService) persistReconciledAccountOpsConfig(ctx context.Context, c AccountOpsConfig) error {
	update := func(lockedCtx context.Context) error { return s.saveConfig(lockedCtx, c) }
	if lock, ok := s.repo.(accountOpsConfigLocker); ok {
		return lock.WithAccountOpsConfigLock(ctx, update)
	}
	return update(ctx)
}

func (s *AccountOpsService) ensureManualAccountOpsGroups(ctx context.Context) (AccountOpsConfig, error) {
	if s.accounts == nil {
		return s.currentConfig(), nil
	}
	accounts, err := s.accounts.ListAllWithFilters(ctx, "", "", "", "", 0, "")
	if err != nil {
		return AccountOpsConfig{}, err
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	c, err := s.loadConfig(ctx)
	if err != nil {
		return AccountOpsConfig{}, err
	}
	changed, err := s.reconcileAccountOpsGroups(ctx, &c, accounts)
	if err != nil {
		return AccountOpsConfig{}, err
	}
	if changed {
		if err := s.persistReconciledAccountOpsConfig(ctx, c); err != nil {
			return AccountOpsConfig{}, err
		}
	}
	s.config.Store(c)
	return c, nil
}
