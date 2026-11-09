package service

import "encoding/json"

const SettingKeySmartOpsHiddenMenuItems = "smart_ops_hidden_menu_items"

// New installations show auto config, priority scheduling, profit operations,
// quality operations, credential operations v2 and pelican tests. Keep this list
// aligned with frontend/src/constants/smartOpsMenu.ts.
func defaultSmartOpsHiddenMenuItems() []string {
	return []string{
		"/admin/lottery",
		"/admin/channel-operations",
		"/admin/controlled-experiments",
		"/admin/account-ops",
		"/admin/token-guard",
		"/admin/group-cache-tests",
		"/admin/request-captures",
		"/admin/harvest-flow",
	}
}

// Unknown paths are retained for forward compatibility; navigation only consults
// this list for the smart operations group. No runtime feature consumes it.
func normalizeSmartOpsHiddenMenuItems(items []string) []string {
	result := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item != "" && !seen[item] {
			result = append(result, item)
			seen[item] = true
		}
	}
	return result
}

func parseSmartOpsHiddenMenuItems(raw string) []string {
	var items []string
	if json.Unmarshal([]byte(raw), &items) != nil || items == nil {
		return defaultSmartOpsHiddenMenuItems()
	}
	return normalizeSmartOpsHiddenMenuItems(items)
}
