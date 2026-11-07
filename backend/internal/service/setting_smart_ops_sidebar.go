package service

import "encoding/json"

const SettingKeySmartOpsHiddenMenuItems = "smart_ops_hidden_menu_items"

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
	if json.Unmarshal([]byte(raw), &items) != nil {
		return []string{}
	}
	return normalizeSmartOpsHiddenMenuItems(items)
}
