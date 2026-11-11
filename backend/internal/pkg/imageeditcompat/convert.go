// Package imageeditcompat adapts single-reference image edits for form-only providers.
package imageeditcompat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
)

const maxImageBytes = 50 << 20
const maxBodyBytes = 128 << 20

// Enabled requires an exact account ID, endpoint, and hostname match.
func Enabled(baseURL, endpoint string, accountID int64, accountIDs string) bool {
	if endpoint != "/v1/images/edits" || strings.TrimSpace(accountIDs) == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil || !strings.EqualFold(u.Hostname(), "rpm.tosky.io") {
		return false
	}
	for _, id := range strings.Split(accountIDs, ",") {
		if strings.TrimSpace(id) == fmt.Sprint(accountID) {
			return true
		}
	}
	return false
}

// Convert preserves fields and file bytes, while changing image[] to image.
// fetch must enforce the gateway's outbound URL, redirect and size policies.
// Multiple references are rejected rather than silently losing extra images at
// an upstream that declares a single UploadFile field.
func Convert(body []byte, contentType string, fetch func(string) ([]byte, error)) ([]byte, string, error) {
	if len(body) > maxBodyBytes {
		return nil, "", fmt.Errorf("image edit request exceeds 128 MiB")
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	imageCount := 0
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("invalid image edit content type: %w", err)
	}
	if mediaType == "multipart/form-data" {
		if params["boundary"] == "" {
			return nil, "", fmt.Errorf("missing multipart boundary")
		}
		r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := r.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, "", fmt.Errorf("read image edit part: %w", err)
			}
			name := part.FormName()
			if name == "image" || name == "image[]" || strings.HasPrefix(name, "image[") {
				if part.FileName() == "" {
					return nil, "", fmt.Errorf("image must be an uploaded file")
				}
				imageCount++
				if imageCount > 1 {
					return nil, "", fmt.Errorf("this upstream compatibility mode requires exactly one reference image")
				}
				name = "image"
			}
			h := make(textproto.MIMEHeader)
			for k, values := range part.Header {
				h[k] = append([]string(nil), values...)
			}
			values := map[string]string{"name": name}
			if part.FileName() != "" {
				values["filename"] = part.FileName()
			}
			h.Set("Content-Disposition", mime.FormatMediaType("form-data", values))
			dst, err := w.CreatePart(h)
			if err != nil {
				return nil, "", err
			}
			n, err := io.Copy(dst, io.LimitReader(part, maxImageBytes+1))
			_ = part.Close()
			if err != nil || n > maxImageBytes {
				return nil, "", fmt.Errorf("invalid or oversized image edit part")
			}
		}
	} else if mediaType == "application/json" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, "", fmt.Errorf("invalid image edit JSON: %w", err)
		}
		var images []struct {
			URL    string `json:"image_url"`
			FileID string `json:"file_id"`
		}
		if err := json.Unmarshal(fields["images"], &images); err != nil {
			return nil, "", fmt.Errorf("images must contain image_url references")
		}
		imageCount = len(images)
		if imageCount != 1 {
			return nil, "", fmt.Errorf("this upstream compatibility mode requires exactly one reference image")
		}
		for _, image := range images {
			if image.FileID != "" || image.URL == "" {
				return nil, "", fmt.Errorf("this upstream requires image_url; file_id cannot be converted")
			}
			if err := writeImage(w, "image", image.URL, fetch); err != nil {
				return nil, "", err
			}
		}
		delete(fields, "images")
		if mask, ok := fields["mask"]; ok {
			if string(mask) != "null" {
				var ref struct {
					URL    string `json:"image_url"`
					FileID string `json:"file_id"`
				}
				if json.Unmarshal(mask, &ref) != nil || ref.URL == "" || ref.FileID != "" {
					return nil, "", fmt.Errorf("mask must contain an image_url")
				}
				if err := writeImage(w, "mask", ref.URL, fetch); err != nil {
					return nil, "", err
				}
			}
			delete(fields, "mask")
		}
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			value := fields[k]
			if string(value) == "null" {
				continue
			}
			var decoded string
			if json.Unmarshal(value, &decoded) != nil {
				decoded = string(value)
			}
			if err := w.WriteField(k, decoded); err != nil {
				return nil, "", err
			}
		}
	} else {
		return nil, "", fmt.Errorf("unsupported image edit content type")
	}
	if imageCount != 1 {
		return nil, "", fmt.Errorf("this upstream compatibility mode requires exactly one reference image")
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	if buf.Len() > maxBodyBytes {
		return nil, "", fmt.Errorf("converted image edit exceeds 128 MiB")
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func writeImage(w *multipart.Writer, field, ref string, fetch func(string) ([]byte, error)) error {
	var data []byte
	var err error
	if strings.HasPrefix(strings.ToLower(ref), "data:") {
		header, encoded, ok := strings.Cut(ref, ",")
		if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") || len(encoded) > base64.StdEncoding.EncodedLen(maxImageBytes) {
			return fmt.Errorf("invalid or oversized image data URL")
		}
		data, err = base64.StdEncoding.DecodeString(encoded)
	} else {
		u, parseErr := url.Parse(ref)
		if parseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || fetch == nil {
			return fmt.Errorf("unsupported image reference URL")
		}
		data, err = fetch(ref)
	}
	if err != nil {
		return fmt.Errorf("load image reference: %w", err)
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return fmt.Errorf("empty or oversized image reference")
	}
	contentType := http.DetectContentType(data)
	ext := ""
	switch contentType {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	default:
		return fmt.Errorf("reference is not a supported image")
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": field, "filename": field + ext}))
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}
