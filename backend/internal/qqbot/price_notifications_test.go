package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func priceTestSnapshot(value string) PriceSnapshot {
	return PriceSnapshot{States: []PriceState{{Key: "channel:1", Kind: "channel", Name: "示例渠道", Fields: []PriceField{
		{Key: "model/input", Model: "示例模型", Label: "输入", Value: value, Unit: "百万 Token"},
		{Key: "model/time", Model: "示例模型", Label: "当前分时倍率", Value: "1×", Scheduled: true},
	}}}}
}

func TestPriceNotificationDiff(t *testing.T) {
	before, after := priceTestSnapshot("USD 2"), priceTestSnapshot("USD 1.5")
	chunks := formatPriceChanges(before, after, true)
	require.Len(t, chunks, 1)
	require.Equal(t, "渠道：示例渠道\n模型：示例模型\n输入：USD 2 → USD 1.5 / 百万 Token", chunks[0])
	require.NotContains(t, chunks[0], "当前分时倍率")
	require.Empty(t, formatPriceChanges(before, before, true))
	after = priceTestSnapshot("USD 2")
	after.States[0].Name = "只改名称"
	require.Empty(t, formatPriceChanges(before, after, true))
	after.States[0].Fields[1].Value = "0.5×"
	require.Empty(t, formatPriceChanges(before, after, false))
	require.Contains(t, formatPriceChanges(before, after, true)[0], "定时切换")
	require.Contains(t, formatPriceChanges(before, after, true)[0], "1× → 0.5×")
	require.Contains(t, formatPriceChanges(after, before, true)[0], "0.5× → 1×")
	require.Empty(t, formatPriceChanges(PriceSnapshot{}, after, true))
}

func TestPriceNotificationGroupRateReportedOnce(t *testing.T) {
	snapshot := func(base, current string) PriceSnapshot {
		return PriceSnapshot{States: []PriceState{{Key: "group:1", Kind: "group", Name: "DeepSeek", Fields: []PriceField{
			{Key: "01_text", Label: "倍率", Value: base},
			{Key: "05_current", Label: "当前倍率", Value: current, Scheduled: true},
		}}}}
	}
	for _, tc := range []struct {
		name                        string
		baseBefore, baseAfter       string
		currentBefore, currentAfter string
		scheduled                   bool
		want                        string
		timed                       bool
	}{
		{name: "base edit", baseBefore: "0.16×", baseAfter: "0.14×", currentBefore: "0.16×", currentAfter: "0.14×", scheduled: true, want: "倍率：0.16× → 0.14×"},
		{name: "base edit during peak", baseBefore: "0.16×", baseAfter: "0.14×", currentBefore: "0.32×", currentAfter: "0.28×", scheduled: true, want: "倍率：0.16× → 0.14×"},
		{name: "base edit without scheduled notices", baseBefore: "0.16×", baseAfter: "0.14×", currentBefore: "0.16×", currentAfter: "0.14×", want: "倍率：0.16× → 0.14×"},
		{name: "peak starts", baseBefore: "0.16×", baseAfter: "0.16×", currentBefore: "0.16×", currentAfter: "0.32×", scheduled: true, want: "倍率：0.16× → 0.32×", timed: true},
		{name: "peak ends", baseBefore: "0.16×", baseAfter: "0.16×", currentBefore: "0.32×", currentAfter: "0.16×", scheduled: true, want: "倍率：0.32× → 0.16×", timed: true},
		{name: "peak ignored when disabled", baseBefore: "0.16×", baseAfter: "0.16×", currentBefore: "0.16×", currentAfter: "0.32×"},
		{name: "unchanged", baseBefore: "0.16×", baseAfter: "0.16×", currentBefore: "0.16×", currentAfter: "0.16×", scheduled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := formatPriceChanges(snapshot(tc.baseBefore, tc.currentBefore), snapshot(tc.baseAfter, tc.currentAfter), tc.scheduled)
			if tc.want == "" {
				require.Empty(t, chunks)
				return
			}
			require.Len(t, chunks, 1)
			require.Contains(t, chunks[0], tc.want)
			require.Equal(t, 1, strings.Count(chunks[0], " → "))
			require.NotContains(t, chunks[0], "当前倍率")
			require.Equal(t, tc.timed, strings.Contains(chunks[0], "定时切换"))
		})
	}
}

func TestPriceNotificationModelRemovalAndBoundedChunks(t *testing.T) {
	before, after := priceTestSnapshot("USD 2"), priceTestSnapshot("USD 1.5")
	before.States[0].Name = "<qqbot-at-everyone /> **渠道**"
	after.States[0].Name = before.States[0].Name
	for i := 0; i < 50; i++ {
		key := strings.Repeat("模", i+1)
		before.States[0].Fields = append(before.States[0].Fields, PriceField{Key: key, Model: key, Label: "输出", Value: "USD 8"})
		after.States[0].Fields = append(after.States[0].Fields, PriceField{Key: key, Model: key, Label: "输出", Value: "USD 4"})
	}
	chunks := formatPriceChanges(before, after, true)
	require.Greater(t, len(chunks), 1)
	for _, chunk := range chunks {
		require.LessOrEqual(t, len([]rune(chunk)), 1800)
		require.NotContains(t, chunk, "<qqbot-at-everyone />")
	}
	require.Equal(t, 50, strings.Count(strings.Join(chunks, ""), "USD 8 → USD 4"))
	before.States[0].Name = strings.Repeat("*", 1000)
	after.States[0].Name = before.States[0].Name
	after.States[0].Fields[0].Model = strings.Repeat("*", 1000)
	after.States[0].Fields[0].Label = strings.Repeat("*", 1000)
	for _, chunk := range formatPriceChanges(before, after, true) {
		require.LessOrEqual(t, len([]rune(chunk)), 1800)
	}
	after.States[0].Fields = after.States[0].Fields[1:]
	require.Contains(t, strings.Join(formatPriceChanges(before, after, true), ""), "USD 2 → 已移除")
}

func TestPriceNotificationGroupModelPricingChanges(t *testing.T) {
	modelFields := func(input string) []PriceField {
		return []PriceField{
			{Key: "model/openai/gpt-5/00_mode", Model: "gpt-5（openai）", Label: "计费方式", Value: "按 Token"},
			{Key: "model/openai/gpt-5/01_input", Model: "gpt-5（openai）", Label: "输入", Value: input, Unit: "百万 Token"},
		}
	}
	snapshot := func(fields []PriceField) PriceSnapshot {
		return PriceSnapshot{States: []PriceState{{Key: "group:1", Kind: "group", Name: "示例分组", Fields: fields}}}
	}

	before := snapshot(modelFields("USD 2"))
	changed := snapshot(modelFields("USD 1.5"))
	require.Equal(t, []string{"分组：示例分组\n模型：gpt-5（openai）\n输入：USD 2 → USD 1.5 / 百万 Token"}, formatPriceChanges(before, changed, true))

	added := snapshot(modelFields("USD 2"))
	require.Contains(t, strings.Join(formatPriceChanges(snapshot(nil), added, true), "\n"), "模型：gpt-5（openai）")

	removed := snapshot(nil)
	require.Contains(t, strings.Join(formatPriceChanges(before, removed, true), "\n"), "输入：USD 2 → 已移除 / 百万 Token")
}

func TestPriceNotificationSnapshotVersionEstablishesBaseline(t *testing.T) {
	previous := PriceSnapshot{Version: 0, States: []PriceState{{Key: "group:1", Kind: "group", Name: "示例分组"}}}
	current := PriceSnapshot{Version: 1, States: []PriceState{{Key: "group:1", Kind: "group", Name: "示例分组", Fields: []PriceField{
		{Key: "model/openai/gpt-5/01_input", Model: "gpt-5（openai）", Label: "输入", Value: "USD 2", Unit: "百万 Token"},
	}}}}
	require.Empty(t, formatPriceChanges(previous, current, true))
}

func TestPriceNotificationLifecycle(t *testing.T) {
	for _, kind := range []string{"group", "channel"} {
		t.Run(kind, func(t *testing.T) {
			snapshot := func(status, rate string) PriceSnapshot {
				return PriceSnapshot{States: []PriceState{{Key: kind + ":1", Kind: kind, Name: "DeepSeek", Status: status, Fields: []PriceField{
					{Key: "01_text", Label: "倍率", Value: rate},
					{Key: "05_current", Label: "倍率", Value: rate, Scheduled: true},
				}}}}
			}
			noun := "分组"
			if kind == "channel" {
				noun = "渠道"
			}
			active := snapshot("active", "0.16×")
			disabled := snapshot("disabled", "0.16×")
			legacy := snapshot("", "0.16×")
			for _, tc := range []struct {
				name          string
				before, after PriceSnapshot
				want          string
			}{
				{name: "delete active", before: active, want: "已删除"},
				{name: "delete disabled", before: disabled, want: "已删除"},
				{name: "delete legacy", before: legacy, want: "已删除"},
				{name: "disable", before: active, after: disabled, want: "状态：启用 → 停用"},
				{name: "enable", before: disabled, after: active, want: "状态：停用 → 启用"},
				{name: "disable legacy", before: legacy, after: disabled, want: "状态：启用 → 停用"},
				{name: "legacy upgrade", before: legacy, after: active},
				{name: "new active baseline", after: active},
				{name: "new disabled baseline", after: disabled},
				{name: "unchanged active", before: active, after: active},
				{name: "unchanged disabled", before: disabled, after: disabled},
				{name: "inactive price edit", before: disabled, after: snapshot("disabled", "0.14×")},
				{name: "enable after price edit", before: disabled, after: snapshot("active", "0.14×"), want: "状态：停用 → 启用"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					for _, scheduled := range []bool{false, true} {
						chunks := formatPriceChanges(tc.before, tc.after, scheduled)
						if tc.want == "" {
							require.Empty(t, chunks)
						} else {
							require.Equal(t, []string{noun + "：DeepSeek\n" + tc.want}, chunks)
						}
					}
				})
			}
		})
	}
}

func TestPriceNotificationConciseGroupAndDefaultValues(t *testing.T) {
	before := PriceSnapshot{States: []PriceState{{Key: "group:1", Kind: "group", Name: "DeepSeek", Fields: []PriceField{
		{Key: "01_text", Label: "倍率", Value: "0.16×"},
		{Key: "02_image", Label: "图片倍率", Value: "跟随基础倍率"},
	}}}}
	after := PriceSnapshot{States: []PriceState{{Key: "group:1", Kind: "group", Name: "DeepSeek", Fields: []PriceField{
		{Key: "01_text", Label: "倍率", Value: "0.14×"},
		{Key: "02_image", Label: "图片倍率", Value: "0.5×"},
	}}}}
	require.Equal(t, []string{"分组：DeepSeek\n倍率：0.16× → 0.14×\n图片倍率：跟随倍率 → 0.5×"}, formatPriceChanges(before, after, true))
	before = priceTestSnapshot("未设置（沿用计费默认值）")
	after = priceTestSnapshot("USD 1.5")
	require.Equal(t, []string{"渠道：示例渠道\n模型：示例模型\n输入：默认 → USD 1.5 / 百万 Token"}, formatPriceChanges(before, after, true))
}

func TestPriceNotificationLifecyclePersistedBaseline(t *testing.T) {
	var baseline PriceSnapshot
	// Existing snapshots have no status field. They represent active entities.
	require.NoError(t, json.Unmarshal([]byte(`{"states":[{"key":"group:1","kind":"group","name":"DeepSeek","fields":[]}]}`), &baseline))
	for _, step := range []struct {
		status string
		want   string
	}{
		{status: "disabled", want: "状态：启用 → 停用"},
		{status: "disabled"},
		{status: "active", want: "状态：停用 → 启用"},
		{want: "已删除"},
		{},
	} {
		current := PriceSnapshot{}
		if step.status != "" {
			current.States = []PriceState{{Key: "group:1", Kind: "group", Name: "DeepSeek", Status: step.status}}
		}
		chunks := formatPriceChanges(baseline, current, false)
		if step.want == "" {
			require.Empty(t, chunks)
		} else {
			require.Equal(t, []string{"分组：DeepSeek\n" + step.want}, chunks)
		}
		raw, err := json.Marshal(current)
		require.NoError(t, err)
		baseline = PriceSnapshot{}
		require.NoError(t, json.Unmarshal(raw, &baseline))
	}
}

func TestPriceNotificationConfigAndProactiveDelivery(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = true
	c.AppID = "app"
	c.AppSecret = "test"
	c.AllowedGroups = []string{"group", "other"}
	c.PriceNotifications = PriceNotificationConfig{Enabled: true, Scheduled: true, Groups: []string{"group", "group"}}
	require.NoError(t, validatePriceNotifications(c))
	require.Equal(t, []string{"group"}, priceNotificationGroups(c))
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":"7200"}`))
			return
		}
		requests++
		require.Equal(t, "/v2/groups/group/messages", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.EqualValues(t, 2, body["msg_type"])
		require.Equal(t, "通知正文", body["markdown"].(map[string]any)["content"])
		for _, key := range []string{"msg_id", "event_id", "msg_seq", "content"} {
			require.NotContains(t, body, key)
		}
		_, _ = w.Write([]byte(`{"id":"notice"}`))
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL = server.URL
	s.tokenURL = server.URL + "/token"
	j := Job{AppID: "app", GroupID: "group", EventType: priceNotificationEvent}
	require.NoError(t, s.sendPriceNotification(context.Background(), c, j, "通知正文"))
	j.GroupID = "other"
	require.ErrorIs(t, s.sendPriceNotification(context.Background(), c, j, "通知正文"), ErrDisabled)
	j.GroupID = "group"
	j.AppID = "another-app"
	require.ErrorIs(t, s.sendPriceNotification(context.Background(), c, j, "通知正文"), ErrDisabled)
	require.Equal(t, 1, requests)
	c.PriceNotifications.Groups = []string{"forbidden"}
	require.Error(t, validatePriceNotifications(c))
}
