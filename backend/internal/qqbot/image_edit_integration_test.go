//go:build integration

package qqbot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotEditsExactReferencedImage(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, source := range []string{"bot", "member", "callback", "second-attachment"} {
			t.Run(protocol+"/"+source, func(t *testing.T) {
				db := testDB(t)
				s := savedBot(t, db)
				ctx := context.Background()
				c, err := s.Config(ctx)
				require.NoError(t, err)
				original := testInputPNG(t)
				other, err := normalizeImage(original)
				require.NoError(t, err)
				var chats, edits, sends, downloads atomic.Int32
				s.imageDownload = func(_ context.Context, url string) ([]byte, error) {
					downloads.Add(1)
					if strings.HasSuffix(url, "/other") {
						return other, nil
					}
					require.Equal(t, "https://image.example/original", url)
					return original, nil
				}
				index := 1
				if source == "second-attachment" {
					index = 2
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/token":
						_, _ = w.Write([]byte(`{"access_token":"qq-test","expires_in":7200}`))
					case "/v1/chat/completions", "/v1/responses":
						chats.Add(1)
						var body map[string]json.RawMessage
						require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						if replyImageReviewTest(t, w, protocol, body) {
							chats.Add(-1)
							return
						}
						key := "messages"
						if protocol == "responses" {
							key = "input"
						}
						require.Contains(t, string(body[key]), base64.StdEncoding.EncodeToString(original))
						require.NotContains(t, string(body[key]), "https://image.example")
						require.Contains(t, string(body["tools"]), chatEditImageToolName)
						_, _ = w.Write(editToolTestJSON(t, protocol, fmt.Sprintf(`{"prompt":"将背景改成蓝色，保留主体和其他细节","image_index":%d}`, index)))
					case "/v1/images/edits":
						edits.Add(1)
						require.NoError(t, r.ParseMultipartForm(20<<20))
						defer r.MultipartForm.RemoveAll()
						file, _, readErr := r.FormFile("image")
						require.NoError(t, readErr)
						defer file.Close()
						got, readErr := io.ReadAll(file)
						require.NoError(t, readErr)
						require.Equal(t, original, got, "edit the exact selected picture, never the newest picture")
						require.Contains(t, r.FormValue("prompt"), "保留主体")
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(other)}}})
					case "/v2/groups/group/files":
						_, _ = w.Write([]byte(`{"file_info":"edited"}`))
					case "/v2/groups/group/messages":
						n := sends.Add(1)
						_, _ = fmt.Fprintf(w, `{"id":"edited-%d","ext_info":{"ref_idx":"edited-ref-%d"}}`, n, n)
					default:
						t.Errorf("unexpected request (including generation fallback): %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				c.BaseURL, c.ChatProtocol, c.ImageEnabled, c.ImageModel, c.HistoryMessages = server.URL+"/v1", protocol, true, "edit-model", 0
				_, err = s.SaveConfig(ctx, c)
				require.NoError(t, err)
				s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
				e := sharedTestEvent("edit-request", "group", "member", "背景换成蓝色，其他保留")
				e.MessageScene.Ext = []string{"ref_msg_idx=original-ref"}
				attachment := Attachment{URL: "https://image.example/original", ContentType: "image/png"}
				eventType := groupAtMessageCreate
				switch source {
				case "bot":
					for i, data := range [][]byte{original, other} {
						id := fmt.Sprintf("source-%d", i)
						_, err = db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,payload,kind,status,result_image) VALUES($1,'app',$1,'GROUP_AT_MESSAGE_CREATE','group','{}','image','completed',$2)`, id, data)
						require.NoError(t, err)
						ref := "original-ref"
						if i == 1 {
							ref = "newer-ref"
						}
						response := []byte(fmt.Sprintf(`{"id":"%s","ext_info":{"ref_idx":"%s"}}`, id, ref))
						require.NoError(t, s.rememberSentMessage(ctx, c, Job{ID: id, GroupID: "group", CreatedAt: time.Now()}, response, "图片已生成。", true, true))
					}
					eventType = groupMessageCreate // Known bot pictures can be quoted without @.
				case "member":
					photo := sharedTestEvent("member-photo", "group", "photographer", "")
					photo.Attachments, photo.MessageScene.Ext = []Attachment{attachment}, []string{"msg_idx=original-ref"}
					enqueueSharedTestEvent(t, s, c, photo, groupMessageCreate)
					require.Zero(t, downloads.Load(), "passive caching must not download images")
					var jobs int
					require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs`).Scan(&jobs))
					require.Zero(t, jobs)
				case "callback":
					e.MsgElements = []MessageElement{{MessageType: 103, Attachments: []Attachment{attachment}}}
				case "second-attachment":
					e.MessageScene.Ext = nil
					e.Attachments = []Attachment{{URL: "https://image.example/other", ContentType: "image/jpeg"}, attachment}
				}
				enqueueSharedTestEvent(t, s, c, e, eventType)
				s.runOne(ctx)
				var status, kind string
				require.NoError(t, db.QueryRow(`SELECT status,kind FROM qq_bot_jobs WHERE message_id='edit-request'`).Scan(&status, &kind))
				require.Equal(t, "completed", status)
				require.Equal(t, "image", kind)
				require.EqualValues(t, 1, chats.Load())
				require.EqualValues(t, 1, edits.Load())
				require.EqualValues(t, 3, sends.Load())
				ref, err := s.referencedMessage(ctx, c, "group", "edited-ref-3")
				require.NoError(t, err)
				require.NotNil(t, ref)
				require.NotEmpty(t, ref.ImageJobID, "the edited result can be quoted for a subsequent edit")
				require.Equal(t, "图片已修改。", ref.Content)
				e.MessageScene.Ext = []string{"ref_msg_idx=edited-ref-3"}
				data, _, err := s.referencedImageSources(ctx, c, e, ref)
				require.NoError(t, err)
				require.Equal(t, "image/jpeg", http.DetectContentType(data))
				var chatCount, imageCount int
				require.NoError(t, db.QueryRow(`SELECT SUM(count) FILTER(WHERE kind='chat'), SUM(count) FILTER(WHERE kind='image') FROM qq_bot_daily_usage WHERE scope='member:member'`).Scan(&chatCount, &imageCount))
				require.Equal(t, 1, chatCount)
				require.Equal(t, 1, imageCount)
			})
		}
	}
}

func TestQQBotImageReferencesAreScopedExpiringAndBounded(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.ImageEnabled, c.ImageModel = true, "image-model"
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	e := sharedTestEvent("photo", "group", "member", "")
	e.MessageScene.Ext = []string{"msg_idx=photo-ref"}
	e.Attachments = []Attachment{{URL: "https://image.example/original", ContentType: "image/png"}}
	require.NoError(t, s.rememberIncomingImages(ctx, c, e))
	quote := sharedTestEvent("quote", "group", "member", "修改")
	quote.MessageScene.Ext = []string{"ref_msg_idx=photo-ref"}
	_, images, err := s.referencedImageSources(ctx, c, quote, nil)
	require.NoError(t, err)
	require.Len(t, images, 1)
	// Quoting a member's photo alone still does not address the bot.
	enqueueSharedTestEvent(t, s, c, quote, groupMessageCreate)
	var jobs int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs`).Scan(&jobs))
	require.Zero(t, jobs)
	for _, changeApp := range []bool{false, true} {
		otherC, otherE := c, quote
		if changeApp {
			otherC.AppID = "another-app"
		} else {
			otherE.GroupID = "other-group"
		}
		_, images, err = s.referencedImageSources(ctx, otherC, otherE, nil)
		require.NoError(t, err)
		require.Empty(t, images)
	}
	_, err = db.Exec(`UPDATE qq_bot_incoming_images SET created_at=NOW()-INTERVAL '25 hours'`)
	require.NoError(t, err)
	require.NoError(t, s.rememberIncomingImages(ctx, c, e))
	_, images, err = s.referencedImageSources(ctx, c, quote, nil)
	require.NoError(t, err)
	require.Empty(t, images, "duplicates cannot revive expired locators")
	for i := 0; i <= maxIncomingImageMessages; i++ {
		e.ID = fmt.Sprintf("photo-%03d", i)
		e.MessageScene.Ext = []string{"msg_idx=" + e.ID}
		require.NoError(t, s.rememberIncomingImages(ctx, c, e))
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_incoming_images`).Scan(&count))
	require.Equal(t, maxIncomingImageMessages, count)
	quote.MessageScene.Ext = []string{"ref_msg_idx=photo-000"}
	_, images, err = s.referencedImageSources(ctx, c, quote, nil)
	require.NoError(t, err)
	require.Empty(t, images)
	_, err = db.Exec(`UPDATE qq_bot_incoming_images SET created_at=NOW()-INTERVAL '25 hours'`)
	require.NoError(t, err)
	s.maintenance(ctx)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_incoming_images`).Scan(&count))
	require.Zero(t, count)
}

func TestQQBotReferenceImageRecoveryFailures(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.ImageEnabled = true
	original := testInputPNG(t)
	photo := sharedTestEvent("photo", "group", "member", "")
	photo.Attachments = []Attachment{{URL: "https://images.example/expired-url", ContentType: "image/png"}}
	require.NoError(t, s.rememberIncomingImages(ctx, c, photo))
	quote := sharedTestEvent("quote", "group", "member", "换个背景")
	quote.MessageReference.MessageID = "photo"
	// Fresh nested quote attachments must replace expired stored locators.
	fresh := Attachment{URL: "https://images.example/fresh", ContentType: "image/png"}
	quote.MsgElements = []MessageElement{{MessageType: 103, Attachments: []Attachment{fresh, fresh}}}
	downloads := 0
	s.imageDownload = func(_ context.Context, url string) ([]byte, error) {
		downloads++
		require.Equal(t, fresh.URL, url)
		return original, nil
	}
	input, err := s.chatMessage(ctx, c, quote, quote.Content, nil)
	require.NoError(t, err)
	require.Len(t, input.Images, 1)
	require.Contains(t, input.Content, "前 1 张")
	require.Equal(t, 1, downloads)

	// Even a corrupted link cannot read another group's/app's generated bytes.
	_, err = db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,payload,kind,status,result_image) VALUES('source','app','source','GROUP_AT_MESSAGE_CREATE','group','{}','image','completed',$1)`, original)
	require.NoError(t, err)
	ref := &botMessageReference{Image: true, ImageJobID: "source"}
	quote.MsgElements = nil
	for _, variant := range []string{"cross-group", "cross-app", "expired", "missing-bytes"} {
		otherC, otherE := c, quote
		switch variant {
		case "cross-group":
			otherE.GroupID = "other-group"
		case "cross-app":
			otherC.AppID = "other-app"
		case "expired":
			_, err = db.Exec(`UPDATE qq_bot_jobs SET created_at=NOW()-INTERVAL '25 hours' WHERE id='source'`)
			require.NoError(t, err)
		case "missing-bytes":
			_, err = db.Exec(`UPDATE qq_bot_jobs SET created_at=NOW(),result_image=NULL WHERE id='source'`)
			require.NoError(t, err)
		}
		input, err = s.chatMessage(ctx, otherC, otherE, otherE.Content, ref)
		require.ErrorContains(t, err, "重新发送原图")
		require.Empty(t, input.Images)
	}
}
