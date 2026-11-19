//go:build unit

package service

import (
	"bytes"
	"context"
	"mime"
	"mime/multipart"
	"net/textproto"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIVideoRequestValidation(t *testing.T) {
	for _, body := range []string{
		`{`,
		"[]",
		`{"model":1,"seconds":5}`,
		`{"model":"example","seconds":0}`,
		`{"model":"example","seconds":1.2}`,
		`{"model":"example","seconds":-2}`,
		`{"model":"example","seconds":5,"duration":6}`,
		`{"model":"example","seconds":5,"n":2}`,
		`{"model":"example","seconds":5,"seconds":30}`,
		`{"model":"example","model":"other","seconds":5}`,
		`{"model":"example","Model":"other","seconds":5}`,
	} {
		_, err := ParseOpenAIVideoRequest("application/json", []byte(body))
		require.Error(t, err, body)
	}
	info, err := ParseOpenAIVideoRequest("application/json", []byte(`{"model":"example","prompt":"waves","duration":"30","size":"720x1280","metadata":{"content":[{"image_url":"https://example.com/ref.png"}]}}`))
	require.NoError(t, err)
	require.Equal(t, 30, info.DurationSeconds)
	require.Equal(t, "720p", info.Resolution)
	require.Equal(t, []string{"https://example.com/ref.png"}, info.InputImageURLs)
}

func TestOpenAIVideoRejectsAmbiguousModerationJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields string
	}{
		{"duplicate_prompt", `"prompt":"waves","prompt":"other"`},
		{"case_prompt", `"prompt":"waves","Prompt":"other"`},
		{"alias_only_prompt", `"PROMPT":"other"`},
		{"escaped_prompt", `"prompt":"waves","pro\u006dpt":"other"`},
		{"unicode_case_seconds", `"ſeconds":30`},
		{"unicode_case_mask", `"maſk":"https://example.com/other.png"`},
		{"duplicate_image", `"image":"a","image":"b"`},
		{"case_image", `"Image":"b"`},
		{"duplicate_images", `"images":["a"],"images":["b"]`},
		{"case_images", `"Images":["b"]`},
		{"duplicate_reference_images", `"reference_images":["a"],"reference_images":["b"]`},
		{"case_reference_images", `"Reference_Images":["b"]`},
		{"duplicate_mask", `"mask":"a","mask":"b"`},
		{"case_mask", `"Mask":"b"`},
		{"duplicate_input_reference", `"input_reference":{"image_url":"a"},"input_reference":{"image_url":"b"}`},
		{"case_input_reference", `"Input_Reference":{"image_url":"b"}`},
		{"duplicate_metadata", `"metadata":{"content":[]},"metadata":{"content":[{"image_url":"b"}]}`},
		{"case_metadata", `"Metadata":{"content":[{"image_url":"b"}]}`},
		{"image_duplicate_url", `"image":{"url":"a","url":"b"}`},
		{"image_case_url", `"image":{"URL":"b"}`},
		{"images_duplicate_image_url", `"images":[{"image_url":"a","image_url":"b"}]`},
		{"images_case_image_url", `"images":[{"Image_URL":"b"}]`},
		{"reference_images_nested_duplicate_url", `"reference_images":[{"image_url":{"url":"a","url":"b"}}]`},
		{"mask_nested_case_url", `"mask":{"image_url":{"URL":"b"}}`},
		{"input_reference_duplicate_image_url", `"input_reference":{"image_url":"a","image_url":"b"}`},
		{"input_reference_case_image_url", `"input_reference":{"Image_URL":"b"}`},
		{"input_reference_nested_duplicate_url", `"input_reference":{"image_url":{"url":"a","url":"b"}}`},
		{"metadata_duplicate_content", `"metadata":{"content":[],"content":[{"image_url":"b"}]}`},
		{"metadata_case_content", `"metadata":{"Content":[{"image_url":"b"}]}`},
		{"metadata_duplicate_image_url", `"metadata":{"content":[{"image_url":"a","image_url":"b"}]}`},
		{"metadata_case_image_url", `"metadata":{"content":[{"Image_URL":"b"}]}`},
		{"metadata_nested_duplicate_url", `"metadata":{"content":[{"image_url":{"url":"a","url":"b"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"example","seconds":5,` + tc.fields + `}`
			_, err := ParseOpenAIVideoRequest("application/json", []byte(body))
			require.Error(t, err)
		})
	}
}

func TestOpenAIVideoModerationJSONPreservesMultipleImages(t *testing.T) {
	body := []byte(`{
		"model":"example","seconds":5,"prompt":"waves",
		"image":[{"url":"https://example.com/a.png"},{"image_url":{"url":"https://example.com/b.png"}}],
		"images":[{"image_url":"https://example.com/c.png"},{"url":"https://example.com/c.png"}],
		"reference_images":["https://example.com/d.png"],
		"mask":{"image_url":{"url":"https://example.com/mask.png"}},
		"input_reference":{"image_url":{"url":"https://example.com/e.png"}},
		"metadata":{"content":[{"image_url":"https://example.com/f.png"},{"image_url":{"url":"https://example.com/g.png"}}],
			"passthrough":1,"passthrough":2,"opaque":{"url":"a","url":"b"}}
	}`)
	info, err := ParseOpenAIVideoRequest("application/json", body)
	require.NoError(t, err)
	require.Equal(t, "waves", info.Prompt)
	require.Equal(t, []string{
		"https://example.com/a.png", "https://example.com/b.png",
		"https://example.com/c.png", "https://example.com/c.png",
		"https://example.com/d.png", "https://example.com/e.png",
		"https://example.com/f.png", "https://example.com/g.png",
	}, info.InputImageURLs)
	require.JSONEq(t, `{"prompt":"waves","images":[
		{"image_url":"https://example.com/a.png"},{"image_url":"https://example.com/b.png"},
		{"image_url":"https://example.com/c.png"},{"image_url":"https://example.com/c.png"},
		{"image_url":"https://example.com/d.png"},{"image_url":"https://example.com/e.png"},
		{"image_url":"https://example.com/f.png"},{"image_url":"https://example.com/g.png"},
		{"image_url":"https://example.com/mask.png"}]}`, string(info.ModerationBody()))
}

type openAIVideoTestPart struct {
	name     string
	value    string
	fileName string
}

func openAIVideoTestMultipart(t *testing.T, parts []openAIVideoTestPart) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "example"))
	require.NoError(t, writer.WriteField("seconds", "5"))
	for _, part := range parts {
		if part.fileName == "" {
			require.NoError(t, writer.WriteField(part.name, part.value))
			continue
		}
		file, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": part.name, "filename": part.fileName})},
			"Content-Type":        {"image/png"},
		})
		require.NoError(t, err)
		_, err = file.Write([]byte(part.value))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return writer.FormDataContentType(), body.Bytes()
}

func TestOpenAIVideoRejectsAmbiguousModerationMultipart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []openAIVideoTestPart
	}{
		{"duplicate_prompt", []openAIVideoTestPart{{"prompt", "waves", ""}, {"prompt", "other", ""}}},
		{"case_prompt", []openAIVideoTestPart{{"prompt", "waves", ""}, {"Prompt", "other", ""}}},
		{"alias_only_prompt", []openAIVideoTestPart{{"PROMPT", "other", ""}}},
		{"prompt_file", []openAIVideoTestPart{{"prompt", "other", "prompt.txt"}}},
		{"duplicate_seconds", []openAIVideoTestPart{{"seconds", "30", ""}}},
		{"seconds_file", []openAIVideoTestPart{{"seconds", "30", "seconds.txt"}}},
		{"unicode_case_mask", []openAIVideoTestPart{{"maſk", "other", "mask.png"}}},
		{"case_image_file", []openAIVideoTestPart{{"Image", "other", "image.png"}}},
		{"case_indexed_image_file", []openAIVideoTestPart{{"Image[0]", "other", "image.png"}}},
		{"indexed_image_text", []openAIVideoTestPart{{"image[]", "https://example.com/a.png", ""}}},
		{"duplicate_mask_text", []openAIVideoTestPart{{"mask", "a", ""}, {"mask", "b", ""}}},
		{"mixed_mask_aliases", []openAIVideoTestPart{{"mask", "a", ""}, {"mask_image_url", "b", ""}}},
		{"duplicate_mask_files", []openAIVideoTestPart{{"mask", "a", "a.png"}, {"mask", "b", "b.png"}}},
		{"mask_text_then_file", []openAIVideoTestPart{{"mask", "a", ""}, {"mask", "b", "b.png"}}},
		{"mask_file_then_text", []openAIVideoTestPart{{"mask", "a", "a.png"}, {"mask", "b", ""}}},
		{"case_input_reference_file", []openAIVideoTestPart{{"Input_Reference", "other", "image.png"}}},
		{"duplicate_input_reference_text", []openAIVideoTestPart{{"input_reference", `{"image_url":"a"}`, ""}, {"input_reference", `{"image_url":"b"}`, ""}}},
		{"input_reference_duplicate_image_url", []openAIVideoTestPart{{"input_reference", `{"image_url":"a","image_url":"b"}`, ""}}},
		{"input_reference_case_url", []openAIVideoTestPart{{"input_reference", `{"image_url":{"URL":"b"}}`, ""}}},
		{"invalid_input_reference_json", []openAIVideoTestPart{{"input_reference", `{`, ""}}},
		{"duplicate_metadata", []openAIVideoTestPart{{"metadata", `{"content":[]}`, ""}, {"metadata", `{"content":[{"image_url":"b"}]}`, ""}}},
		{"case_metadata", []openAIVideoTestPart{{"Metadata", `{"content":[]}`, ""}}},
		{"metadata_file", []openAIVideoTestPart{{"metadata", `{"content":[]}`, "metadata.json"}}},
		{"metadata_duplicate_content", []openAIVideoTestPart{{"metadata", `{"content":[],"content":[{"image_url":"b"}]}`, ""}}},
		{"metadata_case_image_url", []openAIVideoTestPart{{"metadata", `{"content":[{"Image_URL":"b"}]}`, ""}}},
		{"metadata_nested_duplicate_url", []openAIVideoTestPart{{"metadata", `{"content":[{"image_url":{"url":"a","url":"b"}}]}`, ""}}},
		{"invalid_metadata_json", []openAIVideoTestPart{{"metadata", `{`, ""}}},
		{"unparsed_images_text", []openAIVideoTestPart{{"images", `[{"url":"b"}]`, ""}}},
		{"unparsed_reference_images_text", []openAIVideoTestPart{{"reference_images", `["b"]`, ""}}},
		{"unparsed_reference_images_file", []openAIVideoTestPart{{"reference_images", "b", "image.png"}}},
		{"unparsed_first_frame_text", []openAIVideoTestPart{{"first_frame", "b", ""}}},
		{"unparsed_last_frame_text", []openAIVideoTestPart{{"last_frame", "b", ""}}},
		{"unparsed_image_url_file", []openAIVideoTestPart{{"image_url", "b", "image.png"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contentType, body := openAIVideoTestMultipart(t, tc.parts)
			_, err := ParseOpenAIVideoRequest(contentType, body)
			require.Error(t, err)
		})
	}
}

func TestOpenAIVideoModerationMultipartPreservesMultipleImages(t *testing.T) {
	contentType, body := openAIVideoTestMultipart(t, []openAIVideoTestPart{
		{"prompt", "waves", ""},
		{"image", "https://example.com/a.png", ""},
		{"image", "https://example.com/b.png", ""},
		{"image_url", "https://example.com/c.png", ""},
		{"image_url", "https://example.com/c.png", ""},
		{"image", "a", "a.png"},
		{"image", "b", "b.png"},
		{"image[]", "c", "c.png"},
		{"image[0]", "d", "d.png"},
		{"images", "e", "e.png"},
		{"images", "f", "f.png"},
		{"input_reference", "g", "g.png"},
		{"input_reference", "h", "h.png"},
		{"first_frame", "i", "i.png"},
		{"last_frame", "j", "j.png"},
		{"mask", "mask", "mask.png"},
		{"metadata", `{"content":[{"image_url":"https://example.com/d.png"},{"image_url":{"url":"https://example.com/e.png"}}],"passthrough":1,"passthrough":2}`, ""},
	})
	info, err := ParseOpenAIVideoRequest(contentType, body)
	require.NoError(t, err)
	require.Equal(t, "waves", info.Prompt)
	require.Equal(t, []string{
		"https://example.com/a.png", "https://example.com/b.png",
		"https://example.com/c.png", "https://example.com/c.png",
		"https://example.com/d.png", "https://example.com/e.png",
	}, info.InputImageURLs)
	require.Len(t, info.Uploads, 10)
	for i, upload := range info.Uploads {
		require.Equal(t, []byte{byte('a' + i)}, upload.Data)
	}
	require.NotNil(t, info.MaskUpload)
	require.Equal(t, []byte("mask"), info.MaskUpload.Data)
	moderation := info.ModerationBody()
	require.Equal(t, int64(17), gjson.GetBytes(moderation, "images.#").Int())
	require.Contains(t, string(moderation), "data:image/png;base64,YQ==")
	require.Contains(t, string(moderation), "data:image/png;base64,ag==")
	require.Contains(t, string(moderation), "data:image/png;base64,bWFzaw==")
	contentType, body = openAIVideoTestMultipart(t, []openAIVideoTestPart{
		{"input_reference", `{"image_url":{"url":"https://example.com/ref.png"}}`, ""},
		{"mask_image_url", "https://example.com/mask.png", ""},
	})
	info, err = ParseOpenAIVideoRequest(contentType, body)
	require.NoError(t, err)
	require.Equal(t, []string{"https://example.com/ref.png"}, info.InputImageURLs)
	require.Equal(t, "https://example.com/mask.png", info.MaskImageURL)
}

func TestOpenAIVideoAccountOptIn(t *testing.T) {
	account := seedanceTestAccount()
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideos))
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
	account.Credentials["openai_capabilities"] = []string{"seedance", "videos"}
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideos))
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
	account.Type = AccountTypeOAuth
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideos))
}

func TestOpenAIVideoChannelPrice(t *testing.T) {
	for _, tc := range []struct {
		mode BillingMode
		want float64
	}{
		{BillingModeVideo, 12},
		{BillingModePerRequest, 0.4},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			groupID := int64(32)
			price := 0.4
			svc := newOpenAIRecordUsageServiceForTest(nil, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			cache := newEmptyChannelCache()
			cache.channelByGroupID[groupID] = &Channel{ID: groupID, Status: StatusActive}
			cache.pricingByGroupModel[channelModelKey{groupID: groupID, model: "video-model"}] = &ChannelModelPricing{
				BillingMode: tc.mode, PerRequestPrice: &price,
			}
			cache.loadedAt = time.Now()
			channels := &ChannelService{}
			channels.cache.Store(cache)
			svc.resolver = NewModelPricingResolver(channels, svc.billingService)
			key := &APIKey{GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI}}
			result := &OpenAIForwardResult{ResponseID: openAIVideoTaskPrefix + "2014b840-5350-4000-8000-100f38f89d5c",
				VideoCount: 1, VideoDurationSeconds: 30, VideoResolution: "720p"}
			require.InDelta(t, tc.want, svc.calculateOpenAIVideoCost(context.Background(), "video-model", key, result, 1).ActualCost, 1e-9)
		})
	}
}
