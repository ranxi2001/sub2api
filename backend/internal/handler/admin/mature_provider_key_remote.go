// Adapted from mizaawa/zayuapi v0.2.219, a71aa9615ccb55a19973908c9f261b5ba25a48c6.
// LGPL v3; upstream authors retain their copyrights. See mature_provider_NOTICE.md.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Site-specific API key and group operations for Sub2API and New API.

func (s *providerSession) listKeys(ctx context.Context) ([]providerKeyView, error) {
	if s.record.SiteType == "sub2api" {
		return s.listSub2APIKeys(ctx)
	}
	return s.listNewAPIKeys(ctx)
}

func (s *providerSession) listGroups(ctx context.Context) ([]providerGroupView, error) {
	var groups []providerGroupView
	var err error
	if s.record.SiteType == "sub2api" {
		groups, err = s.sub2apiGroups(ctx, s.sub2apiGroupRates(ctx))
	} else {
		groups, err = s.newAPIGroups(ctx)
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(groups))
	for _, group := range groups {
		if seen[group.ID] {
			return nil, errProviderResponse
		}
		seen[group.ID] = true
	}
	return groups, nil
}

func (s *providerSession) createKey(ctx context.Context, group providerGroupView) (providerCreatedKey, error) {
	if s.record.SiteType == "sub2api" {
		return s.createSub2APIKey(ctx, group)
	}
	return s.createNewAPIKey(ctx, group)
}

// resolveKey returns an active key together with its complete secret.
func (s *providerSession) resolveKey(ctx context.Context, keyID string) (providerKeyView, error) {
	keys, err := s.listKeys(ctx)
	if err != nil {
		return providerKeyView{}, err
	}
	for _, key := range keys {
		if key.ID != keyID {
			continue
		}
		if !key.Available {
			return providerKeyView{}, errProviderKeyUnavailable
		}
		if s.record.SiteType == "newapi" {
			if !completeProviderSecret(key.secret) {
				// Current New API releases mask keys in the list response.
				if key.secret, err = s.newAPIKeySecret(ctx, key.ID); err != nil {
					return providerKeyView{}, err
				}
			}
			// New API stores keys without their sk- display prefix.
			if key.secret != "" && !strings.HasPrefix(key.secret, "sk-") {
				key.secret = "sk-" + key.secret
			}
		}
		if !completeProviderSecret(key.secret) {
			return providerKeyView{}, errProviderKeySecretUnavailable
		}
		return key, nil
	}
	return providerKeyView{}, errProviderKeyMissing
}

func (s *providerSession) listSub2APIKeys(ctx context.Context) ([]providerKeyView, error) {
	rows, err := s.listRows(ctx, func(page int) string {
		return fmt.Sprintf("/api/v1/keys?page=%d&page_size=%d&sort_by=created_at&sort_order=desc", page, providerKeyPageSize)
	}, false)
	if err != nil {
		return nil, err
	}
	rates := s.sub2apiGroupRates(ctx)
	keys := make([]providerKeyView, 0, len(rows))
	missingGroups := false
	for _, row := range rows {
		key := sub2apiKeyView(row, rates)
		missingGroups = missingGroups || (key.GroupID != "" && key.GroupName == "")
		keys = append(keys, key)
	}
	if missingGroups {
		// Some releases list keys without their group; enrich them best effort.
		if groups, groupErr := s.sub2apiGroups(ctx, rates); groupErr == nil {
			byID := make(map[string]providerGroupView, len(groups))
			for _, group := range groups {
				byID[group.ID] = group
			}
			for i := range keys {
				group, found := byID[keys[i].GroupID]
				if !found || keys[i].GroupName != "" {
					continue
				}
				keys[i].GroupName, keys[i].GroupPlatform = group.Name, group.Platform
				if keys[i].GroupMultiplier == nil {
					keys[i].GroupMultiplier = group.Multiplier
				}
			}
		}
	}
	return keys, nil
}

func sub2apiKeyView(item map[string]any, rates map[string]float64) providerKeyView {
	status := normalizeProviderKeyStatus(providerText(item["status"]))
	if status == "active" {
		if expires, ok := item["expires_at"].(string); ok && expires != "" {
			if at, err := time.Parse(time.RFC3339Nano, expires); err == nil && !at.After(time.Now()) {
				status = "expired"
			}
		}
		quota, limited := number(item["quota"])
		used, _ := number(item["quota_used"])
		if limited && quota > 0 && used >= quota {
			status = "quota_exhausted"
		}
	}
	group, _ := item["group"].(map[string]any)
	groupID := providerText(item["group_id"])
	if groupID == "" {
		groupID = providerText(group["id"])
	}
	multiplier := providerMultiplier(group["rate_multiplier"])
	if rate, ok := rates[groupID]; ok {
		multiplier = &rate
	}
	return providerKeyView{
		ID: providerText(item["id"]),
		// Sub2API HTML-escapes key names when it stores them.
		Name:            cleanProviderText(html.UnescapeString(providerText(item["name"])), 120),
		GroupID:         groupID,
		GroupName:       cleanProviderText(providerText(group["name"]), 120),
		GroupPlatform:   cleanProviderText(providerText(group["platform"]), 32),
		GroupMultiplier: multiplier,
		Status:          status,
		Available:       status == "active",
		secret:          providerText(item["key"]),
	}
}

// sub2apiGroupRates returns the account's own group rates. They only refine
// the group defaults, so a failure is not fatal.
func (s *providerSession) sub2apiGroupRates(ctx context.Context) map[string]float64 {
	rates := make(map[string]float64)
	data, _, err := s.call(ctx, http.MethodGet, "/api/v1/groups/rates", nil, nil, true)
	if err != nil {
		return rates
	}
	if values, ok := data.(map[string]any); ok {
		for id, raw := range values {
			if rate := providerMultiplier(raw); rate != nil {
				rates[strings.TrimSpace(id)] = *rate
			}
		}
	}
	return rates
}

func (s *providerSession) sub2apiGroups(ctx context.Context, rates map[string]float64) ([]providerGroupView, error) {
	data, _, err := s.call(ctx, http.MethodGet, "/api/v1/groups/available", nil, nil, true)
	if err != nil {
		return nil, err
	}
	list, ok := parseProviderList(data)
	if !ok {
		return nil, errProviderResponse
	}
	groups := make([]providerGroupView, 0, len(list.items))
	for _, item := range list.items {
		id := providerText(item["id"])
		if value, err := strconv.ParseInt(id, 10, 64); err != nil || value <= 0 {
			return nil, errProviderResponse
		}
		name := cleanProviderText(providerText(item["name"]), 120)
		if name == "" {
			return nil, errProviderResponse
		}
		if status := providerText(item["status"]); status != "" && status != "active" {
			continue
		}
		multiplier := providerMultiplier(item["rate_multiplier"])
		if rate, ok := rates[id]; ok {
			multiplier = &rate
		}
		groups = append(groups, providerGroupView{
			ID: id, Name: name, Platform: cleanProviderText(providerText(item["platform"]), 32),
			Description: cleanProviderText(providerText(item["description"]), 160),
			Multiplier:  multiplier, KeyName: providerKeyName("sub2api", name),
		})
	}
	return groups, nil
}

func (s *providerSession) createSub2APIKey(ctx context.Context, group providerGroupView) (providerCreatedKey, error) {
	groupID, err := strconv.ParseInt(group.ID, 10, 64)
	if err != nil || groupID <= 0 {
		return providerCreatedKey{}, errProviderGroupUnavailable
	}
	// Sites that enforce idempotency reject a creation without this key.
	header := http.Header{}
	header.Set("Idempotency-Key", "provider-key-"+uuid.NewString())
	data, _, err := s.call(ctx, http.MethodPost, "/api/v1/keys", map[string]any{"name": group.KeyName, "group_id": groupID}, header, false)
	if err != nil {
		return providerCreatedKey{}, providerCreateFailure(err)
	}
	created := providerCreatedKey{name: group.KeyName}
	if row, ok := data.(map[string]any); ok {
		if id := providerText(row["id"]); validProviderKeyID(id) {
			created.keyID = id
		}
		if name := cleanProviderText(html.UnescapeString(providerText(row["name"])), 120); name != "" {
			created.name = name
		}
	}
	return created, nil
}

func (s *providerSession) newAPIKeyRows(ctx context.Context) ([]map[string]any, error) {
	return s.listRows(ctx, func(page int) string {
		return fmt.Sprintf("/api/token/?p=%d&page_size=%d", page, providerKeyPageSize)
	}, true)
}

func (s *providerSession) listNewAPIKeys(ctx context.Context) ([]providerKeyView, error) {
	rows, err := s.newAPIKeyRows(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]providerKeyView, 0, len(rows))
	needsUserGroup := false
	for _, row := range rows {
		key := newAPIKeyView(row)
		needsUserGroup = needsUserGroup || key.GroupID == ""
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return keys, nil
	}
	// Ratios and the account's default group are display metadata only.
	ratios := make(map[string]*float64)
	if groups, groupErr := s.newAPIGroups(ctx); groupErr == nil {
		for _, group := range groups {
			ratios[group.ID] = group.Multiplier
		}
	}
	userGroup := ""
	if needsUserGroup {
		userGroup = s.newAPIUserGroup(ctx)
	}
	for i := range keys {
		group := keys[i].GroupID
		if group == "" {
			// A token without a group uses the account's own group.
			group = userGroup
		}
		keys[i].GroupName = cleanProviderText(group, 120)
		keys[i].GroupMultiplier = ratios[group]
	}
	return keys, nil
}

func newAPIKeyView(item map[string]any) providerKeyView {
	status := normalizeProviderKeyStatus(providerText(item["status"]))
	if status == "active" {
		// expired_time is a Unix timestamp; -1 means the token never expires.
		if expires, ok := number(item["expired_time"]); ok && expires > 0 && expires <= float64(time.Now().Unix()) {
			status = "expired"
		}
		unlimited, _ := item["unlimited_quota"].(bool)
		if remain, ok := number(item["remain_quota"]); ok && !unlimited && remain <= 0 {
			status = "quota_exhausted"
		}
	}
	return providerKeyView{
		ID:        providerText(item["id"]),
		Name:      cleanProviderText(providerText(item["name"]), 120),
		GroupID:   providerText(item["group"]),
		Status:    status,
		Available: status == "active",
		secret:    providerText(item["key"]),
	}
}

func (s *providerSession) newAPIGroups(ctx context.Context) ([]providerGroupView, error) {
	data, _, err := s.call(ctx, http.MethodGet, "/api/user/self/groups", nil, nil, true)
	var remote *providerRemoteError
	if errors.As(err, &remote) && remote.status == http.StatusNotFound {
		// Releases before /self/groups expose the user's groups here.
		data, _, err = s.call(ctx, http.MethodGet, "/api/user/groups", nil, nil, true)
	}
	if err != nil {
		return nil, err
	}
	groups := make([]providerGroupView, 0)
	add := func(id string, detail any) {
		if !validProviderGroupID(id) {
			return
		}
		group := providerGroupView{ID: id, Name: cleanProviderText(id, 120)}
		if values, ok := detail.(map[string]any); ok {
			group.Multiplier = providerMultiplier(values["ratio"])
			group.Description = cleanProviderText(providerText(values["desc"]), 160)
		} else {
			group.Multiplier = providerMultiplier(detail)
		}
		group.KeyName = providerKeyName("newapi", group.Name)
		groups = append(groups, group)
	}
	switch values := data.(type) {
	case map[string]any:
		for id, detail := range values {
			add(id, detail)
		}
	case []any:
		for _, raw := range values {
			if row, ok := raw.(map[string]any); ok {
				add(providerText(row["name"]), row)
			} else {
				add(providerText(raw), nil)
			}
		}
	default:
		return nil, errProviderResponse
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups, nil
}

func (s *providerSession) newAPIUserGroup(ctx context.Context) string {
	data, _, err := s.call(ctx, http.MethodGet, "/api/user/self", nil, nil, true)
	if err != nil {
		return ""
	}
	profile, _ := data.(map[string]any)
	return providerText(profile["group"])
}

func (s *providerSession) createNewAPIKey(ctx context.Context, group providerGroupView) (providerCreatedKey, error) {
	// Older releases acknowledge a creation without returning the token, so
	// remember the existing IDs to recognize the new row afterwards.
	before, err := s.newAPIKeyRows(ctx)
	if err != nil {
		return providerCreatedKey{}, err
	}
	known := make(map[string]bool, len(before))
	for _, row := range before {
		known[providerText(row["id"])] = true
	}
	body := map[string]any{
		"name": group.KeyName, "group": group.ID, "remain_quota": 0, "unlimited_quota": true, "expired_time": -1,
		"model_limits_enabled": false, "model_limits": "", "allow_ips": "", "cross_group_retry": false,
	}
	data, _, err := s.call(ctx, http.MethodPost, "/api/token/", body, nil, false)
	if err != nil {
		return providerCreatedKey{}, providerCreateFailure(err)
	}
	created := providerCreatedKey{name: group.KeyName}
	if row, ok := data.(map[string]any); ok {
		if id := providerText(row["id"]); validProviderKeyID(id) {
			created.keyID = id
		}
	}
	if created.keyID == "" {
		if after, listErr := s.newAPIKeyRows(ctx); listErr == nil {
			created.keyID = newestCreatedKeyID(after, known, group.KeyName)
		}
	}
	return created, nil
}

// newAPIKeySecret reads one complete key. Current releases mask keys in the
// list and reveal a single key through this endpoint.
func (s *providerSession) newAPIKeySecret(ctx context.Context, keyID string) (string, error) {
	path := "/api/token/" + url.PathEscape(keyID) + "/key"
	data, _, err := s.call(ctx, http.MethodPost, path, map[string]any{}, nil, true)
	var remote *providerRemoteError
	if errors.As(err, &remote) && (remote.status == http.StatusNotFound || remote.status == http.StatusMethodNotAllowed) {
		data, _, err = s.call(ctx, http.MethodGet, path, nil, nil, true)
		if errors.As(err, &remote) && (remote.status == http.StatusNotFound || remote.status == http.StatusMethodNotAllowed) {
			return "", errProviderKeySecretUnavailable
		}
	}
	if err != nil {
		return "", err
	}
	switch value := data.(type) {
	case string:
		return strings.TrimSpace(value), nil
	case map[string]any:
		for _, field := range []string{"key", "token", "api_key"} {
			if secret := providerText(value[field]); secret != "" {
				return secret, nil
			}
		}
	}
	return "", errProviderKeySecretUnavailable
}

type providerList struct {
	items  []map[string]any
	total  int
	legacy bool
}

// listRows walks a paginated list. A page without new IDs ends the walk, so a
// site that ignores the page parameter cannot loop forever.
func (s *providerSession) listRows(ctx context.Context, pathFor func(page int) string, zeroBasedLegacy bool) ([]map[string]any, error) {
	page := 1
	current, err := s.listPage(ctx, pathFor(page))
	if err != nil {
		return nil, err
	}
	if current.legacy && zeroBasedLegacy {
		// Legacy New API pages are numbered from zero and ignore page_size.
		page = 0
		if current, err = s.listPage(ctx, pathFor(page)); err != nil {
			return nil, err
		}
	}
	rows := make([]map[string]any, 0, len(current.items))
	seen := make(map[string]bool)
	for fetched := 1; ; fetched++ {
		added := 0
		for _, item := range current.items {
			id := providerText(item["id"])
			if !validProviderKeyID(id) {
				return nil, errProviderResponse
			}
			if !seen[id] {
				seen[id] = true
				rows = append(rows, item)
				added++
			}
		}
		// An upstream may cap page_size below our requested size without
		// returning total. A short page does not prove there is no next page.
		if added == 0 || (current.total >= 0 && len(rows) >= current.total) {
			return rows, nil
		}
		if fetched >= providerKeyMaxPages {
			return nil, errProviderKeyListTooLarge
		}
		page++
		if current, err = s.listPage(ctx, pathFor(page)); err != nil {
			return nil, err
		}
	}
}

func (s *providerSession) listPage(ctx context.Context, path string) (providerList, error) {
	data, _, err := s.call(ctx, http.MethodGet, path, nil, nil, true)
	if err != nil {
		return providerList{}, err
	}
	list, ok := parseProviderList(data)
	if !ok {
		return providerList{}, errProviderResponse
	}
	return list, nil
}

// parseProviderList accepts a paginated object ({items, total}) or a bare
// array, which legacy New API releases return without pagination metadata.
func parseProviderList(data any) (providerList, bool) {
	switch value := data.(type) {
	case nil:
		return providerList{total: -1, legacy: true}, true
	case []any:
		items, ok := providerObjects(value)
		return providerList{items: items, total: -1, legacy: true}, ok
	case map[string]any:
		total := -1
		if count, ok := number(value["total"]); ok && count >= 0 && count < math.MaxInt32 {
			total = int(count)
		}
		for _, field := range []string{"items", "list", "records", "data"} {
			raw, exists := value[field]
			if !exists {
				continue
			}
			if raw == nil {
				return providerList{total: total}, true
			}
			list, isList := raw.([]any)
			if !isList {
				return providerList{}, false
			}
			items, ok := providerObjects(list)
			return providerList{items: items, total: total}, ok
		}
	}
	return providerList{}, false
}

func providerObjects(list []any) ([]map[string]any, bool) {
	items := make([]map[string]any, 0, len(list))
	for _, raw := range list {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		items = append(items, item)
	}
	return items, true
}

// newestCreatedKeyID finds the row that appeared under the created name after
// a creation request; the highest numeric ID wins a tie.
func newestCreatedKeyID(rows []map[string]any, known map[string]bool, name string) string {
	best, bestValue := "", int64(-1)
	for _, row := range rows {
		id := providerText(row["id"])
		if known[id] || !validProviderKeyID(id) || providerText(row["name"]) != name {
			continue
		}
		value, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			value = 0
		}
		if best == "" || value > bestValue {
			best, bestValue = id, value
		}
	}
	return best
}

// providerKeyName names a key created from this panel. New API rejects token
// names longer than 30 bytes; Sub2API stores at most 100 bytes after it has
// HTML-escaped the name.
func providerKeyName(siteType, groupName string) string {
	name := providerKeyNamePrefix + strings.TrimSpace(groupName)
	fits := func(candidate string) bool {
		if siteType == "newapi" {
			return len(candidate) <= 30
		}
		return len(html.EscapeString(candidate)) <= 100
	}
	for !fits(name) {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

func normalizeProviderKeyStatus(raw string) string {
	status := strings.ToLower(strings.TrimSpace(raw))
	switch status {
	case "1", "enabled", "active", "normal":
		return "active"
	case "2", "inactive", "disabled":
		return "disabled"
	case "3", "expired":
		return "expired"
	case "4", "quota_exhausted", "exhausted":
		return "quota_exhausted"
	case "":
		return "unknown"
	default:
		return cleanProviderText(status, 32)
	}
}

// completeProviderSecret rejects masked or malformed keys so that a
// placeholder can never be saved as an account credential.
func completeProviderSecret(secret string) bool {
	if secret == "" || len(secret) > 4096 || strings.Contains(secret, "*") || strings.Contains(secret, "...") {
		return false
	}
	for _, r := range secret {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validProviderKeyID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func validProviderGroupID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && strings.IndexFunc(id, unicode.IsControl) < 0
}

// providerText returns a compact string for an identifier-like JSON value.
func providerText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func providerMultiplier(raw any) *float64 {
	value, ok := number(raw)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e6 {
		return nil
	}
	return &value
}

// cleanProviderText strips control characters, collapses whitespace and
// bounds text received from a provider before it is displayed or stored.
func cleanProviderText(value string, limit int) string {
	value = strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(value) > limit {
		value = string([]rune(value)[:limit]) + "…"
	}
	return value
}
