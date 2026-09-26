package service

// ExcelBPS403DisabledAtKey records when an upstream HTTP 403 automatically
// turned Excel BPS off (RFC 3339, UTC). Only DisableExcelBPSOn403 writes it; the
// admin account list shows it as a suspected Excel ban.
const ExcelBPS403DisabledAtKey = "openai_excel_bps_403_disabled_at"

// ExcelBPS403MovedAtKey and ExcelBPS403MovedGroupIDKey record the last automatic
// group action after an upstream HTTP 403 (group 0 means all groups were left).
// Only MoveExcelBPSOn403 writes them; the account list flags the suspected ban
// while the account's groups still match that action.
const (
	ExcelBPS403MovedAtKey      = "openai_excel_bps_403_moved_at"
	ExcelBPS403MovedGroupIDKey = "openai_excel_bps_403_moved_group_id"
)

var excelBPS403MarkerKeys = []string{ExcelBPS403DisabledAtKey, ExcelBPS403MovedAtKey, ExcelBPS403MovedGroupIDKey}

// MergeExcelBPS403Marker keeps the persisted 403 records across account edits
// and ignores values supplied by the edit. Turning Excel BPS back on
// acknowledges the automatic shutdown and clears its record; group-action
// records stay until the next automatic move. The repository applies this
// under the row lock.
func MergeExcelBPS403Marker(extra, current map[string]any) map[string]any {
	for _, key := range excelBPS403MarkerKeys {
		delete(extra, key)
	}
	enabled, _ := extra["openai_excel_bps"].(bool)
	for _, key := range excelBPS403MarkerKeys {
		value, ok := current[key]
		if !ok || (enabled && key == ExcelBPS403DisabledAtKey) {
			continue
		}
		if extra == nil {
			extra = make(map[string]any, len(excelBPS403MarkerKeys))
		}
		extra[key] = value
	}
	return extra
}
