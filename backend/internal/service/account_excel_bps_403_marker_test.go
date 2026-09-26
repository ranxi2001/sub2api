package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeExcelBPS403Marker(t *testing.T) {
	const at = "2026-09-26T15:04:05Z"
	stored := map[string]any{"openai_excel_bps": false, ExcelBPS403DisabledAtKey: at}
	for _, tc := range []struct {
		name    string
		extra   map[string]any
		current map[string]any
		want    map[string]any
	}{
		{
			name:    "ordinary edit keeps stored marker",
			extra:   map[string]any{"openai_passthrough": true},
			current: stored,
			want:    map[string]any{"openai_passthrough": true, ExcelBPS403DisabledAtKey: at},
		},
		{
			name:    "explicitly disabled protocol keeps marker",
			extra:   map[string]any{"openai_excel_bps": false},
			current: stored,
			want:    map[string]any{"openai_excel_bps": false, ExcelBPS403DisabledAtKey: at},
		},
		{
			name:    "edit cannot replace marker",
			extra:   map[string]any{ExcelBPS403DisabledAtKey: "2000-01-01T00:00:00Z"},
			current: stored,
			want:    map[string]any{ExcelBPS403DisabledAtKey: at},
		},
		{
			name:    "edit cannot add marker",
			extra:   map[string]any{ExcelBPS403DisabledAtKey: at},
			current: map[string]any{"openai_excel_bps": false},
			want:    map[string]any{},
		},
		{
			name:    "re-enabling protocol clears marker",
			extra:   map[string]any{"openai_excel_bps": true, ExcelBPS403DisabledAtKey: at},
			current: stored,
			want:    map[string]any{"openai_excel_bps": true},
		},
		{
			name:    "non-boolean switch does not clear marker",
			extra:   map[string]any{"openai_excel_bps": "true"},
			current: stored,
			want:    map[string]any{"openai_excel_bps": "true", ExcelBPS403DisabledAtKey: at},
		},
		{
			name:    "nil edit keeps marker",
			current: stored,
			want:    map[string]any{ExcelBPS403DisabledAtKey: at},
		},
		{
			name: "nil maps",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, MergeExcelBPS403Marker(tc.extra, tc.current))
		})
	}
}

func TestMergeExcelBPS403MoveMarker(t *testing.T) {
	const at = "2026-09-27T01:02:03Z"
	stored := map[string]any{"openai_excel_bps": true, ExcelBPS403MovedAtKey: at, ExcelBPS403MovedGroupIDKey: float64(7)}
	for _, tc := range []struct {
		name    string
		extra   map[string]any
		current map[string]any
		want    map[string]any
	}{
		{
			name:    "ordinary edit keeps group action",
			extra:   map[string]any{"openai_excel_bps": true},
			current: stored,
			want:    map[string]any{"openai_excel_bps": true, ExcelBPS403MovedAtKey: at, ExcelBPS403MovedGroupIDKey: float64(7)},
		},
		{
			name:    "edit cannot rewrite group action",
			extra:   map[string]any{ExcelBPS403MovedAtKey: "2000-01-01T00:00:00Z", ExcelBPS403MovedGroupIDKey: float64(9)},
			current: stored,
			want:    map[string]any{ExcelBPS403MovedAtKey: at, ExcelBPS403MovedGroupIDKey: float64(7)},
		},
		{
			name:    "edit cannot add group action",
			extra:   map[string]any{ExcelBPS403MovedAtKey: at, ExcelBPS403MovedGroupIDKey: float64(0)},
			current: map[string]any{},
			want:    map[string]any{},
		},
		{
			name:    "re-enabling clears only the shutdown record",
			extra:   map[string]any{"openai_excel_bps": true},
			current: map[string]any{ExcelBPS403DisabledAtKey: at, ExcelBPS403MovedAtKey: at, ExcelBPS403MovedGroupIDKey: float64(0)},
			want:    map[string]any{"openai_excel_bps": true, ExcelBPS403MovedAtKey: at, ExcelBPS403MovedGroupIDKey: float64(0)},
		},
		{
			name:    "nil edit keeps group action",
			current: stored,
			want:    map[string]any{ExcelBPS403MovedAtKey: at, ExcelBPS403MovedGroupIDKey: float64(7)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, MergeExcelBPS403Marker(tc.extra, tc.current))
		})
	}
}
