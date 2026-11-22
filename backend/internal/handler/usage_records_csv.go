package handler

import (
	"bytes"
	"encoding/csv"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/language"
)

var usageCSVLanguages = language.NewMatcher([]language.Tag{language.English, language.Chinese})

func usageCSVString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func usageCSVLatency(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func usageCSVEffort(value *string) string {
	raw := strings.TrimSpace(usageCSVString(value))
	normalized := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, raw)
	switch normalized {
	case "", "none", "minimal":
		return "-"
	case "low", "medium", "high", "max":
		return strings.ToUpper(normalized[:1]) + normalized[1:]
	case "extrahigh", "xhigh":
		return "XHigh"
	default:
		r, size := utf8.DecodeRuneInString(raw)
		return string(unicode.ToUpper(r)) + raw[size:]
	}
}

func usageCSVCacheRate(row usageRecord) string {
	total := float64(row.InputTokens) + float64(row.CacheReadTokens) + float64(row.CacheCreationTokens)
	if total <= 0 || row.InputTokens < 0 || row.CacheReadTokens < 0 || row.CacheCreationTokens < 0 {
		return ""
	}
	if row.CacheReadTokens == 0 {
		return "0%"
	}
	tenths := math.Round(float64(row.CacheReadTokens) / total * 1000)
	switch tenths {
	case 0:
		return "<0.1%"
	case 1000:
		return "100%"
	default:
		return strconv.FormatFloat(tenths/10, 'f', 1, 64) + "%"
	}
}

func usageCSVBillingMode(row usageRecord, zh bool) string {
	mode := usageCSVString(row.BillingMode)
	if mode == "" && row.ImageCount > 0 {
		mode = "image"
	}
	switch mode {
	case "per_request":
		if zh {
			return "按次"
		}
		return "Per Request"
	case "image":
		if zh {
			return "按次(图片)"
		}
		return "Image"
	case "video":
		if zh {
			return "按次(视频)"
		}
		return "Video"
	default:
		if zh {
			return "按量"
		}
		return "Token"
	}
}

// JavaScript toFixed rounds the exact binary value, with ties away from zero.
func usageCSVCost(value float64) string {
	switch {
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	case math.IsNaN(value), math.Abs(value) >= 1e21:
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
	return new(big.Rat).SetFloat64(value).FloatString(8)
}

// Keep column order, optional columns, eight decimal places, BOM and spreadsheet
// formula protection in sync with exportToCSV in the user's UsageView.
func usageRecordsCSV(data []usageRecord, keyName string, showModel, showCacheRate bool, acceptLanguage string) []byte {
	var buf bytes.Buffer
	_, _ = buf.WriteString("\uFEFF")
	w := csv.NewWriter(&buf)
	header := []string{"Time", "API Key Name", "Model"}
	if showModel {
		header = append(header, "Upstream Response Model")
	}
	header = append(header, "Reasoning Effort", "Inbound Endpoint", "IP Address", "Type", "Billing Mode",
		"Input Tokens", "Output Tokens", "Cache Read Tokens", "Cache Creation Tokens")
	if showCacheRate {
		header = append(header, "Cache Rate")
	}
	header = append(header, "Rate Multiplier", "Billed Cost", "Original Cost", "First Token (ms)", "Duration (ms)")
	_ = w.Write(header) // bytes.Buffer writes cannot fail.
	_, locale := language.MatchStrings(usageCSVLanguages, acceptLanguage)
	types := map[string]string{"cyber": "Cyber", "live": "Live", "ws_v2": "WS", "stream": "Stream", "sync": "Sync"}
	for _, row := range data {
		cells := []string{row.CreatedAt.Format(time.RFC3339Nano), keyName, row.Model}
		if showModel {
			cells = append(cells, usageCSVString(row.UpstreamResponseModel))
		}
		requestType := types[row.RequestType]
		if requestType == "" {
			requestType = "Unknown"
		}
		cells = append(cells, usageCSVEffort(row.ReasoningEffort), usageCSVString(row.InboundEndpoint), usageCSVString(row.IPAddress),
			requestType, usageCSVBillingMode(row, locale == 1), strconv.Itoa(row.InputTokens), strconv.Itoa(row.OutputTokens),
			strconv.Itoa(row.CacheReadTokens), strconv.Itoa(row.CacheCreationTokens))
		if showCacheRate {
			cells = append(cells, usageCSVCacheRate(row))
		}
		cells = append(cells, strconv.FormatFloat(row.RateMultiplier, 'f', -1, 64),
			usageCSVCost(row.ActualCost), usageCSVCost(row.TotalCost),
			usageCSVLatency(row.FirstTokenMs), usageCSVLatency(row.DurationMs))
		for i, cell := range cells {
			if cell != "" && cell != "-" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
				cells[i] = "'" + cell
			}
		}
		_ = w.Write(cells)
	}
	w.Flush()
	return buf.Bytes()
}
