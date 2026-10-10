package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	UpstreamBillingSiteExtraKey   = "upstream_billing_site"
	UpstreamBillingUserIDExtraKey = "upstream_billing_user_id"
)

// AccountOpsGroup is a persisted, manually managed account group.
type AccountOpsGroup struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Provider   string  `json:"provider,omitempty"`
	Site       string  `json:"site,omitempty"`
	AccountIDs []int64 `json:"account_ids,omitempty"`
}

type AccountOpsGroupAssignment struct {
	ID          string
	Name        string
	DefaultName string
	Provider    string
	Site        string
	Mode        string
}

type AccountOpsGroupView struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DefaultName string  `json:"default_name"`
	Provider    string  `json:"provider"`
	Site        string  `json:"site"`
	Mode        string  `json:"mode"`
	AccountIDs  []int64 `json:"account_ids"`
}

type accountOpsGroupCacheEntry struct {
	assignment AccountOpsGroupAssignment
	expiresAt  time.Time
}

// accountOpsGroupAssignment resolves only persisted membership. Automatic
// discovery is kept in legacyAccountOpsGroupAssignment for one-time bootstrap.
func accountOpsGroupAssignment(a *Account, c AccountOpsConfig) AccountOpsGroupAssignment {
	if a == nil {
		return AccountOpsGroupAssignment{}
	}
	for _, group := range c.Groups {
		for _, id := range group.AccountIDs {
			if id == a.ID {
				name := strings.TrimSpace(group.Name)
				if name == "" {
					name = a.Name
				}
				provider, site, _ := accountOpsUpstreamIdentity(a)
				if group.Provider != "" {
					provider = group.Provider
				}
				if group.Site != "" {
					site = canonicalAccountOpsSite(group.Site)
				}
				return AccountOpsGroupAssignment{ID: group.ID, Name: name, DefaultName: name, Provider: provider, Site: site, Mode: "manual"}
			}
		}
	}
	provider, site, _ := accountOpsUpstreamIdentity(a)
	id := "account:" + strconv.FormatInt(a.ID, 10)
	return AccountOpsGroupAssignment{ID: id, Name: a.Name, DefaultName: a.Name, Provider: provider, Site: site, Mode: "account"}
}

// legacyAccountOpsGroupAssignment reproduces the former automatic grouping
// rules. It is used only to materialize the existing configuration into manual
// groups during the migration.
func legacyAccountOpsGroupAssignment(a *Account, c AccountOpsConfig) AccountOpsGroupAssignment {
	if a == nil {
		return AccountOpsGroupAssignment{}
	}
	for _, group := range c.Groups {
		for _, id := range group.AccountIDs {
			if id == a.ID {
				name := strings.TrimSpace(group.Name)
				if name == "" {
					name = a.Name
				}
				provider, site, _ := accountOpsUpstreamIdentity(a)
				return AccountOpsGroupAssignment{ID: group.ID, Name: name, DefaultName: name, Provider: provider, Site: site, Mode: "manual"}
			}
		}
	}

	provider, site, userID := accountOpsUpstreamIdentity(a)
	if site == "" || (provider != "new_api" && upstreamBillingProbeTargetIsOfficialAPI(site)) {
		id := "account:" + strconv.FormatInt(a.ID, 10)
		assignment := AccountOpsGroupAssignment{ID: id, Name: a.Name, DefaultName: a.Name, Provider: provider, Site: site, Mode: "account"}
		for _, group := range c.Groups {
			if group.ID == id && strings.TrimSpace(group.Name) != "" {
				assignment.Name = strings.TrimSpace(group.Name)
				break
			}
		}
		return assignment
	}
	identity := provider + "|" + site
	if provider == "new_api" {
		if userID <= 0 {
			id := "account:" + strconv.FormatInt(a.ID, 10)
			assignment := AccountOpsGroupAssignment{ID: id, Name: a.Name, DefaultName: a.Name, Provider: provider, Site: site, Mode: "account"}
			for _, group := range c.Groups {
				if group.ID == id && strings.TrimSpace(group.Name) != "" {
					assignment.Name = strings.TrimSpace(group.Name)
					break
				}
			}
			return assignment
		}
		identity += "|" + strconv.FormatInt(userID, 10)
	}
	hash := sha256.Sum256([]byte(identity))
	id := "auto:" + hex.EncodeToString(hash[:])[:32]
	assignment := AccountOpsGroupAssignment{ID: id, Name: site, DefaultName: site, Provider: provider, Site: site, Mode: "auto"}
	for _, group := range c.Groups {
		if group.ID == id && strings.TrimSpace(group.Name) != "" {
			assignment.Name = strings.TrimSpace(group.Name)
			break
		}
	}
	return assignment
}

func (s *AccountOpsService) legacyAccountOpsGroupAssignment(ctx context.Context, a *Account, c AccountOpsConfig) AccountOpsGroupAssignment {
	assignment := legacyAccountOpsGroupAssignment(a, c)
	if a == nil || s == nil || s.newAPIGroupReader == nil || assignment.Site == "" || upstreamBillingProbeTargetIsOfficialAPI(assignment.Site) {
		return assignment
	}
	if provider, _, userID := accountOpsUpstreamIdentity(a); provider == "new_api" && userID > 0 {
		return assignment
	}
	if cached, ok := s.newAPIGroupCache.Load(a.ID); ok {
		if entry, ok := cached.(accountOpsGroupCacheEntry); ok && time.Now().Before(entry.expiresAt) {
			return entry.assignment
		}
		s.newAPIGroupCache.Delete(a.ID)
	}
	binding, err := s.newAPIGroupReader.GetBinding(ctx, a.ID)
	if err != nil || binding == nil || binding.Profile.UserID <= 0 {
		s.newAPIGroupCache.Store(a.ID, accountOpsGroupCacheEntry{assignment: assignment, expiresAt: time.Now().Add(2 * time.Minute)})
		return assignment
	}
	clone := *a
	clone.Extra = map[string]any{}
	for key, value := range a.Extra {
		clone.Extra[key] = value
	}
	clone.Extra[UpstreamBillingProviderExtraKey] = "new_api"
	clone.Extra[UpstreamBillingSiteExtraKey] = binding.Profile.SiteURL
	clone.Extra[UpstreamBillingUserIDExtraKey] = binding.Profile.UserID
	resolved := legacyAccountOpsGroupAssignment(&clone, c)
	s.newAPIGroupCache.Store(a.ID, accountOpsGroupCacheEntry{assignment: resolved, expiresAt: time.Now().Add(2 * time.Minute)})
	return resolved
}

func (s *AccountOpsService) accountOpsGroupAssignment(_ context.Context, a *Account, c AccountOpsConfig) AccountOpsGroupAssignment {
	return accountOpsGroupAssignment(a, c)
}

func (s *AccountOpsService) cachedAccountOpsGroupAssignment(a *Account, c AccountOpsConfig) AccountOpsGroupAssignment {
	return accountOpsGroupAssignment(a, c)
}

func accountOpsUpstreamIdentity(a *Account) (provider, site string, userID int64) {
	provider, _ = a.Extra[UpstreamBillingProviderExtraKey].(string)
	site, _ = a.Extra[UpstreamBillingSiteExtraKey].(string)
	if site == "" {
		site = canonicalAccountOpsSite(a.GetCredential("base_url"))
	} else {
		site = canonicalAccountOpsSite(site)
	}
	if provider == "" {
		provider = "sub2api"
	}
	if raw, ok := a.Extra[UpstreamBillingUserIDExtraKey]; ok {
		switch v := raw.(type) {
		case float64:
			userID = int64(v)
		case int64:
			userID = v
		case int:
			userID = int64(v)
		case string:
			userID, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	return provider, site, userID
}

func canonicalAccountOpsSite(raw string) string {
	site, err := CanonicalNewAPISite(raw)
	if err != nil {
		return ""
	}
	return strings.TrimRight(site, "/")
}

func (a AccountOpsGroupAssignment) String() string {
	return fmt.Sprintf("%s:%s", a.Provider, a.ID)
}
