package qqbot

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testAnnouncementRequest() AnnouncementRequest {
	return AnnouncementRequest{AppID: "app", Groups: []string{"group"}, Text: "维护公告\n今晚升级", RequestID: "12345678-1234-4234-8234-123456789abc"}
}

func TestAnnouncementValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*AnnouncementRequest)
	}{
		{"empty groups", func(r *AnnouncementRequest) { r.Groups = nil }},
		{"invalid group", func(r *AnnouncementRequest) { r.Groups = []string{"group\nother"} }},
		{"too many groups", func(r *AnnouncementRequest) { r.Groups = make([]string, 101) }},
		{"empty text", func(r *AnnouncementRequest) { r.Text = " \r\n\t" }},
		{"long text", func(r *AnnouncementRequest) { r.Text = strings.Repeat("中", 1601) }},
		{"missing app", func(r *AnnouncementRequest) { r.AppID = "" }},
		{"invalid request ID", func(r *AnnouncementRequest) { r.RequestID = "%_" }},
		{"mention tag", func(r *AnnouncementRequest) { r.Text = "<qqbot-at-everyone />hello" }},
		{"case insensitive mention", func(r *AnnouncementRequest) { r.Text = "<QQBOT-at-user id=\"a\" />" }},
		{"legacy mention", func(r *AnnouncementRequest) { r.Text = "<@all>hello" }},
		{"legacy everyone", func(r *AnnouncementRequest) { r.Text = "@everyone hello" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testAnnouncementRequest()
			tc.edit(&r)
			_, err := normalizeAnnouncement(r)
			require.True(t, errors.Is(err, ErrAnnouncementInvalid), "%v", err)
		})
	}
	r := testAnnouncementRequest()
	r.Text = strings.Repeat("😀", 1600)
	r.Groups = []string{"other-group", "group", "group"}
	normalized, err := normalizeAnnouncement(r)
	require.NoError(t, err)
	require.Equal(t, []string{"group", "other-group"}, normalized.Groups)
	require.Equal(t, []string{"other-group", "group", "group"}, r.Groups, "normalization must not mutate caller's groups")
}
