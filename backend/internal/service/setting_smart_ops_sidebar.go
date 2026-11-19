package service

import "encoding/json"

const SettingKeySmartOpsHiddenMenuItems = "smart_ops_hidden_menu_items"

// New installations show auto config, priority scheduling, channel configuration,
// quality operations, credential operations v2 and pelican tests. Keep this list
// aligned with frontend/src/constants/smartOpsMenu.ts.
func defaultSmartOpsHiddenMenuItems() []string {
	return []string{
		"/admin/lottery",
		"/admin/qqbot/overview",
		"/admin/controlled-experiments",
		"/admin/group-cache-tests",
		"/admin/request-captures",
	}
}

// Unknown paths are retained for forward compatibility; navigation only consults
// this list for the smart operations group. No runtime feature consumes it.
func normalizeSmartOpsHiddenMenuItems(items []string) []string {
	result := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		switch item {
		case "/admin/channel-operations", "/admin/profit-operations", "/admin/account-ops":
			// The three former switches now control one sidebar entry.
		default:
			result = append(result, item)
		}
	}
	// Keep the merged entry visible if any of its former entries was visible.
	if !seen["/admin/channel-config"] && seen["/admin/channel-operations"] &&
		seen["/admin/profit-operations"] && seen["/admin/account-ops"] {
		result = append(result, "/admin/channel-config")
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
