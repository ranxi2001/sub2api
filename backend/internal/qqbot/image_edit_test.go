package qqbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func editToolTestJSON(t *testing.T, protocol, args string) []byte {
	t.Helper()
	return bytes.ReplaceAll(imageToolTestJSON(t, protocol, args), []byte(chatImageToolName), []byte(chatEditImageToolName))
}

func TestImageEditSendsOriginalMultipartAndDoesNotFallback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "upstream-failure"}[fail], func(t *testing.T) {
			original := testInputPNG(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, "/v1/images/edits", r.URL.Path)
				require.Equal(t, "Bearer image-only-key", r.Header.Get("Authorization"))
				require.NoError(t, r.ParseMultipartForm(20<<20))
				defer func() { _ = r.MultipartForm.RemoveAll() }()
				require.Equal(t, "make the background blue; keep the cat", r.FormValue("prompt"))
				require.Equal(t, "image-model", r.FormValue("model"))
				require.Equal(t, "1", r.FormValue("n"))
				file, header, err := r.FormFile("image")
				require.NoError(t, err)
				defer func() { _ = file.Close() }()
				require.Equal(t, "image/png", header.Header.Get("Content-Type"))
				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.Equal(t, original, data)
				if fail {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(original)}}})
			}))
			defer server.Close()
			s := New(nil, nil, false, nil)
			c := DefaultConfig()
			c.BaseURL, c.APIKey = "https://chat.example.invalid", "must-not-use-chat-key"
			c.ImageBaseURL, c.ImageAPIKey, c.ImageModel = server.URL+"/v1", "image-only-key", "image-model"
			dataURL, err := inputImageDataURL(original)
			require.NoError(t, err)
			result, err := s.editImage(context.Background(), c, "make the background blue; keep the cat", dataURL)
			if fail {
				require.Error(t, err)
				require.Empty(t, result)
			} else {
				require.NoError(t, err)
				require.Equal(t, "image/jpeg", http.DetectContentType(result))
			}
			require.Equal(t, 1, calls)
			_, err = s.editImage(context.Background(), c, "edit", "https://untrusted.example/image")
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestChatEditToolSelectionAndImageBounds(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, args := range []string{`{"prompt":"make it blue","image_index":2}`, `{"prompt":"make it blue"}`, `{"prompt":"edit","image_index":0}`, `{"prompt":"edit","image_index":-1}`, `{"prompt":"edit","image_index":5}`, `{"prompt":"edit","image_index":1.5}`, `{"prompt":"edit","image_index":null}`, `{"prompt":"edit","image_index":"1"}`} {
			for _, stream := range []bool{false, true} {
				raw, ct := editToolTestJSON(t, protocol, args), "application/json"
				if stream {
					raw = bytes.ReplaceAll(imageToolTestStream(t, protocol, args), []byte(chatImageToolName), []byte(chatEditImageToolName))
					ct = "text/event-stream"
				}
				got, err := parseChatTurn(raw, ct)
				if strings.Contains(args, `"image_index":2`) {
					require.NoError(t, err)
					require.Equal(t, 2, got.EditImageIndex)
				} else {
					require.Error(t, err)
				}
			}
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Len(t, body["tools"], 2, "old history images cannot become editable current input")
			_, _ = w.Write(editToolTestJSON(t, protocol, `{"prompt":"make it blue","image_index":1}`))
		}))
		c := DefaultConfig()
		c.BaseURL, c.ImageEnabled, c.ChatProtocol = server.URL, true, protocol
		s := New(nil, nil, false, nil)
		_, err := s.chatTurn(context.Background(), c, []Message{{Role: "user", Content: "old", Images: []string{"old-image"}}, {Role: "user", Content: "edit"}})
		require.ErrorContains(t, err, "未取得指定的编辑原图")
		server.Close()
	}
}

func TestOwnImagesExcludeNestedQuotes(t *testing.T) {
	current := Attachment{URL: "https://images.example/current", ContentType: "image/png"}
	quoted := Attachment{URL: "https://images.example/quoted", ContentType: "image/png"}
	e := Event{Attachments: []Attachment{current}, MsgElements: []MessageElement{{MessageType: 103, Attachments: []Attachment{quoted}, MsgElements: []MessageElement{{Attachments: []Attachment{quoted}}}}}}
	require.Equal(t, []Attachment{current}, ownImageAttachments(e))
}
