package qqbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// The model selects only a numbered, validated image from the current input.
// It cannot choose a download URL, filesystem path, API key or upstream model.
func (s *Service) editImage(ctx context.Context, c Config, prompt, dataURL string) ([]byte, error) {
	if err := checkModelBudget(ctx, minimumImageBudget); err != nil {
		return nil, err
	}
	ctx, cancel, budgetErr := modelContext(ctx)
	if budgetErr != nil {
		return nil, budgetErr
	}
	defer cancel()
	header, encoded, ok := strings.Cut(dataURL, ",")
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") || len(encoded) > base64.StdEncoding.EncodedLen(10<<20) {
		return nil, errors.New("编辑原图无效，请重新发送图片。")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("编辑原图无效，请重新发送图片。")
	}
	if _, err = inputImageDataURL(data); err != nil {
		return nil, err
	}
	mime := http.DetectContentType(data)
	if mime == "image/gif" {
		data, err = normalizeImage(data)
		if err != nil {
			return nil, err
		}
		mime = "image/jpeg"
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[mime]
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if c.ImageOutputFormat != "" {
		if err = writer.WriteField("output_format", c.ImageOutputFormat); err != nil {
			return nil, err
		}
	}
	for _, field := range [][2]string{{"model", c.ImageModel}, {"prompt", prompt}, {"n", "1"}, {"size", c.ImageSize}} {
		if err = writer.WriteField(field[0], field[1]); err != nil {
			return nil, err
		}
	}
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="image"; filename="source.` + ext + `"`}, "Content-Type": {mime}})
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(data); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	base, key := imageConnection(c)
	raw, _, err := s.requestBody(ctx, http.MethodPost, endpoint(base, "/images/edits"), key, &body, writer.FormDataContentType(), 32<<20)
	if err != nil {
		return nil, err
	}
	result, err := imageResult(ctx, raw)
	if err != nil {
		return nil, err
	}
	return checkRequestedImageFormat(result, c.ImageOutputFormat)
}
