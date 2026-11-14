package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func testInputPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 8))))
	return data.Bytes()
}

func TestChatImageWireProtocols(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			s := New(nil, nil, false, nil)
			s.imageDownload = func(_ context.Context, url string) ([]byte, error) {
				require.Equal(t, "https://image.example/picture", url)
				return testInputPNG(t), nil
			}
			input, err := s.chatMessage(context.Background(), DefaultConfig(), Event{Attachments: []Attachment{{URL: "//image.example/picture", ContentType: "image/png"}}}, "看图", nil)
			require.NoError(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "Bearer test-only", r.Header.Get("Authorization"))
				var body map[string]json.RawMessage
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				key, textType, imageType := "messages", "text", "image_url"
				if protocol == "responses" {
					key, textType, imageType = "input", "input_text", "input_image"
				}
				var messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}
				require.NoError(t, json.Unmarshal(body[key], &messages))
				require.Len(t, messages, 3)
				require.JSONEq(t, `"rules"`, string(messages[0].Content))
				require.JSONEq(t, `"earlier answer"`, string(messages[1].Content))
				var parts []map[string]any
				require.NoError(t, json.Unmarshal(messages[2].Content, &parts))
				require.Len(t, parts, 2)
				require.Equal(t, textType, parts[0]["type"])
				require.Equal(t, "看图", parts[0]["text"])
				require.Equal(t, imageType, parts[1]["type"])
				var imageURL string
				if protocol == "responses" {
					value, ok := parts[1]["image_url"].(string)
					require.True(t, ok)
					imageURL = value
				} else {
					imagePart, ok := parts[1]["image_url"].(map[string]any)
					require.True(t, ok)
					value, ok := imagePart["url"].(string)
					require.True(t, ok)
					imageURL = value
				}
				require.Contains(t, imageURL, "data:image/png;base64,")
				require.NotContains(t, string(body[key]), "image.example")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"图片中的文字"}}]}`))
			}))
			defer server.Close()
			c := DefaultConfig()
			c.BaseURL, c.APIKey, c.ChatProtocol = server.URL+"/v1", "test-only", protocol
			text, _, _, err := s.chat(context.Background(), c, []Message{{Role: "system", Content: "rules"}, {Role: "assistant", Content: "earlier answer"}, input})
			require.NoError(t, err)
			require.Equal(t, "图片中的文字", text)
		})
	}
}

func TestChatImageValidationAndNestedQuotes(t *testing.T) {
	s := New(nil, nil, false, nil)
	var downloads int
	s.imageDownload = func(_ context.Context, url string) ([]byte, error) {
		downloads++
		if url == "https://image.example/broken" {
			return nil, errors.New("secret signed URL must not escape")
		}
		if url == "https://image.example/invalid" {
			return []byte("not an image"), nil
		}
		return testInputPNG(t), nil
	}
	e := Event{MessageType: 103, MessageScene: MessageScene{Ext: []string{"ref_msg_idx=REF==", "auth_token=private"}}, MsgElements: []MessageElement{{Content: "被引用的原文", MsgElements: []MessageElement{{Attachments: []Attachment{{URL: "https://image.example/ok", ContentType: "image/png"}}}}}}}
	input, err := s.chatMessage(context.Background(), DefaultConfig(), e, "解释一下", &botMessageReference{Content: "确切的机器人回复"})
	require.NoError(t, err)
	require.Len(t, input.Images, 1)
	require.Contains(t, input.Content, "确切的机器人回复")
	require.Contains(t, input.Content, "被引用的原文")
	require.Contains(t, input.Content, "解释一下")
	require.NotContains(t, input.Content, "private")
	require.Equal(t, "REF==", e.referenceID())
	for _, tc := range []struct {
		url  string
		size int64
		want string
	}{
		{"https://image.example/broken", 0, "图片读取失败"},
		{"https://image.example/invalid", 0, "图片格式不受支持"},
		{"https://image.example/ok", 11 << 20, "10 MB"},
	} {
		_, err := s.chatMessage(context.Background(), DefaultConfig(), Event{Attachments: []Attachment{{URL: tc.url, Size: tc.size, ContentType: "image/png"}}}, "看图", nil)
		require.ErrorContains(t, err, tc.want)
	}
	before := downloads
	_, err = s.chatMessage(context.Background(), DefaultConfig(), Event{Attachments: []Attachment{
		{ContentType: "image/png"}, {ContentType: "image/png"}, {ContentType: "image/png"}, {ContentType: "image/png"}, {ContentType: "image/png"},
	}}, "看图", nil)
	require.ErrorContains(t, err, "4 张")
	require.Equal(t, before, downloads)
	input, err = s.chatMessage(context.Background(), DefaultConfig(), Event{MessageScene: e.MessageScene}, "解释一下", nil)
	require.NoError(t, err)
	require.Contains(t, input.Content, "未找到被引用消息")
}

func TestInputImageDownloaderRejectsPrivateAndNonHTTPS(t *testing.T) {
	for _, url := range []string{"http://example.com/image", "https://127.0.0.1/image", "https://[::1]/image", "https://user:pass@example.com/image", "file:///tmp/image"} {
		_, err := downloadImage(context.Background(), url)
		require.Error(t, err)
	}
}
