package qqbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testAnimatedGIF(t *testing.T, frames, width, height int) []byte {
	t.Helper()
	palette := color.Palette{color.Transparent, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	animation := &gif.GIF{LoopCount: 3}
	for i := 0; i < frames; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, width, height), palette)
		frame.SetColorIndex(i%width, 0, uint8(1+i%2))
		animation.Image = append(animation.Image, frame)
		animation.Delay = append(animation.Delay, 7+i)
		animation.Disposal = append(animation.Disposal, gif.DisposalBackground)
	}
	var data bytes.Buffer
	require.NoError(t, gif.EncodeAll(&data, animation))
	return data.Bytes()
}

func TestGIFOutputPreservesAnimationAndStaticCompatibility(t *testing.T) {
	data := testAnimatedGIF(t, 3, 8, 8)
	result, err := normalizeGeneratedImage(data)
	require.NoError(t, err)
	require.Equal(t, data, result)
	animation, err := gif.DecodeAll(bytes.NewReader(result))
	require.NoError(t, err)
	require.Len(t, animation.Image, 3)
	require.Equal(t, []int{7, 8, 9}, animation.Delay)
	require.Equal(t, 3, animation.LoopCount)
	require.Equal(t, []byte{gif.DisposalBackground, gif.DisposalBackground, gif.DisposalBackground}, animation.Disposal)
	require.Equal(t, "image/gif", generatedImageType(result))
	require.Equal(t, "ctav.gif", generatedImageFileName(result))
	still, err := normalizeGeneratedImage(testInputPNG(t))
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", generatedImageType(still))
	require.Equal(t, "ctav.jpg", generatedImageFileName(still))
	_, err = checkRequestedImageFormat(still, "gif")
	require.ErrorContains(t, err, "没有返回 GIF")
	require.NotEmpty(t, imageOutputMessage(err))
	// Existing editing deliberately uses only the first frame.
	first, err := normalizeImage(data)
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", generatedImageType(first))
}

func TestGIFOutputRejectsUnsafeOrMalformedAnimation(t *testing.T) {
	valid := testAnimatedGIF(t, 2, 8, 8)
	for i := 0; i < len(valid); i++ {
		require.Error(t, validateGIFBudget(valid[:i]), "truncated at %d", i)
	}
	for name, data := range map[string][]byte{
		"too-many-frames":  testAnimatedGIF(t, maxGIFFrames+1, 1, 1),
		"wide-canvas":      testAnimatedGIF(t, 1, 2049, 1),
		"too-many-pixels":  testAnimatedGIF(t, 20, 2048, 2048),
		"trailing-payload": append(append([]byte{}, valid...), []byte("unexpected")...),
		"too-large":        append(append([]byte{}, valid...), make([]byte, maxGeneratedImageBytes)...),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeGeneratedImage(data)
			require.Error(t, err)
			require.Empty(t, got)
			require.NotEmpty(t, imageOutputMessage(err))
		})
	}
	// Complete block structure with invalid LZW data must fail DecodeAll too.
	bad := append([]byte{}, valid...)
	i := bytes.Index(bad, []byte{0x2c})
	require.Positive(t, i)
	codeSize := i + 10
	if packed := bad[i+9]; packed&0x80 != 0 {
		codeSize += 3 * (1 << ((packed & 7) + 1))
	}
	bad[codeSize] = 0xff
	_, err := normalizeGeneratedImage(bad)
	require.Error(t, err)
}

func TestGIFGenerateEditAndQQDeliveryPreserveBytes(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "edit"}[edit], func(t *testing.T) {
			data := testAnimatedGIF(t, 3, 8, 8)
			calls, uploads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/images/generations", "/v1/images/edits":
					calls++
					require.Equal(t, "Bearer image-key", r.Header.Get("Authorization"))
					if edit {
						require.Equal(t, "/v1/images/edits", r.URL.Path)
						require.NoError(t, r.ParseMultipartForm(1<<20))
						defer r.MultipartForm.RemoveAll()
						require.Equal(t, "gif", r.FormValue("output_format"))
						f, header, err := r.FormFile("image")
						require.NoError(t, err)
						defer f.Close()
						require.Equal(t, "image/jpeg", header.Header.Get("Content-Type"))
						source, err := io.ReadAll(f)
						require.NoError(t, err)
						require.Equal(t, "image/jpeg", http.DetectContentType(source))
					} else {
						var request map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
						require.Equal(t, "gif", request["output_format"])
						require.Equal(t, "gpt-image-2.6", request["model"])
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"token","expires_in":7200}`))
				case "/v2/groups/group/files":
					uploads++
					var request map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					require.Equal(t, "ctav.gif", request["file_name"])
					require.EqualValues(t, 1, request["file_type"])
					require.Equal(t, base64.StdEncoding.EncodeToString(data), request["file_data"])
					if uploads == 1 {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = w.Write([]byte(`{"file_info":"gif-file"}`))
				case "/v2/groups/group/messages":
					reply := readQQTestReply(t, r)
					require.Equal(t, 7, reply.MsgType)
					require.Equal(t, "gif-file", reply.Media.FileInfo)
					_, _ = w.Write([]byte(`{"id":"sent"}`))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			s := New(nil, nil, false, nil)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			c := DefaultConfig()
			c.BaseURL, c.APIKey, c.ImageModel, c.ImageOutputFormat = server.URL+"/v1", "image-key", "gpt-image-2.6", "gif"
			var result []byte
			var err error
			if edit {
				source, e := inputImageDataURL(data)
				require.NoError(t, e)
				result, err = s.editImage(context.Background(), c, "animate", source)
			} else {
				result, err = s.generateImage(context.Background(), c, "animate")
			}
			require.NoError(t, err)
			require.Equal(t, data, result)
			j := Job{GroupID: "group", MessageID: "msg", CreatedAt: time.Now()}
			require.Error(t, s.send(context.Background(), c, j, "", result, 2))
			require.NoError(t, s.send(context.Background(), c, j, "", result, 3))
			require.Equal(t, 1, calls)
			require.Equal(t, 2, uploads)
		})
	}
}

func TestGIFConfigCommandsAndChatFormats(t *testing.T) {
	c := DefaultConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"image_model":"old-model"}`), &c))
	require.Empty(t, c.ImageOutputFormat)
	c, prompt := imageCommandOptions(c, "--gif waving cat")
	require.Equal(t, "gif", c.ImageOutputFormat)
	require.Equal(t, "waving cat", prompt)
	require.NoError(t, validateConfig(c))
	merged, err := mergeConfigSection(DefaultConfig(), c, "images")
	require.NoError(t, err)
	require.Equal(t, "gif", merged.ImageOutputFormat)
	static, prompt := imageCommandOptions(c, "--static still cat")
	require.Equal(t, "png", static.ImageOutputFormat)
	require.Equal(t, "still cat", prompt)
	_, prompt = imageCommandOptions(c, "--gif")
	require.Empty(t, prompt)
	unchanged, prompt := imageCommandOptions(c, "a gif icon")
	require.Equal(t, c, unchanged)
	require.Equal(t, "a gif icon", prompt)
	c.ImageOutputFormat = "mp4"
	require.Error(t, validateConfig(c))
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, format := range []string{"auto", "gif", "png", "webp", "jpeg", "mp4"} {
				args, _ := json.Marshal(map[string]string{"prompt": "a waving cat", "output_format": format})
				raw, ct := imageToolTestJSON(t, protocol, string(args)), "application/json"
				if stream {
					raw, ct = imageToolTestStream(t, protocol, string(args)), "text/event-stream"
				}
				turn, err := parseChatTurn(raw, ct)
				if format == "mp4" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Equal(t, format, turn.ImageOutputFormat)
				}
			}
		}
	}
}
