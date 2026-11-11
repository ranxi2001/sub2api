package imageeditcompat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func unpack(t *testing.T, body []byte, ct string) (map[string]string, map[string][]byte) {
	t.Helper()
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatal(err)
	}
	r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	fields, files := map[string]string{}, map[string][]byte{}
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		if p.FileName() == "" {
			fields[p.FormName()] = string(b)
		} else {
			files[p.FormName()] = b
		}
	}
	return fields, files
}

func TestJSONToFormPreservesFieldsAndMask(t *testing.T) {
	png := pngBytes(t)
	ref := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	b, _ := json.Marshal(map[string]any{"model": "mapped-image-model", "prompt": "黄色圆点\nKeep blue", "size": "2048x2048", "quality": "auto", "n": 1, "stream": false, "output_compression": 0, "custom": map[string]string{"a": "b"}, "images": []any{map[string]string{"image_url": ref}}, "mask": map[string]string{"image_url": ref}})
	out, ct, err := Convert(b, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	f, files := unpack(t, out, ct)
	for k, want := range map[string]string{"model": "mapped-image-model", "prompt": "黄色圆点\nKeep blue", "size": "2048x2048", "quality": "auto", "n": "1", "stream": "false", "output_compression": "0", "custom": "{\"a\":\"b\"}"} {
		if f[k] != want {
			t.Errorf("%s=%q, want %q", k, f[k], want)
		}
	}
	if !bytes.Equal(files["image"], png) || !bytes.Equal(files["mask"], png) {
		t.Fatal("image or mask changed")
	}
}

func TestMultipartRenamePreservesBytes(t *testing.T) {
	for _, name := range []string{"image", "image[]", "image[0]"} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			w := multipart.NewWriter(&b)
			_ = w.WriteField("prompt", "exact prompt")
			_ = w.WriteField("size", "2048x2048")
			png := pngBytes(t)
			part, _ := w.CreateFormFile(name, "test.png")
			_, _ = part.Write(png)
			mask, _ := w.CreateFormFile("mask", "mask.png")
			_, _ = mask.Write(png)
			_ = w.Close()
			out, ct, err := Convert(b.Bytes(), w.FormDataContentType(), nil)
			if err != nil {
				t.Fatal(err)
			}
			f, files := unpack(t, out, ct)
			if f["prompt"] != "exact prompt" || f["size"] != "2048x2048" || !bytes.Equal(files["image"], png) || !bytes.Equal(files["mask"], png) {
				t.Fatal("fields lost or bytes changed")
			}
		})
	}
}

func TestRemoteImageUsesGuardedFetcher(t *testing.T) {
	b := []byte(`{"prompt":"test","images":[{"image_url":"https://public.example/image.png"}]}`)
	called := false
	out, ct, err := Convert(b, "application/json", func(ref string) ([]byte, error) {
		called = true
		if ref != "https://public.example/image.png" {
			t.Fatal(ref)
		}
		return pngBytes(t), nil
	})
	if err != nil || !called {
		t.Fatalf("called=%v err=%v", called, err)
	}
	_, files := unpack(t, out, ct)
	if len(files["image"]) == 0 {
		t.Fatal("missing image")
	}
	_, _, err = Convert(b, "application/json", func(string) ([]byte, error) { return nil, fmt.Errorf("blocked host") })
	if err == nil || !strings.Contains(err.Error(), "blocked host") {
		t.Fatal("fetch restriction lost")
	}
}

func TestRejectsUnsupportedInputsWithoutDroppingReferences(t *testing.T) {
	for _, body := range []string{
		`{"images":[]}`,
		`{"images":[{"image_url":"data:image/png;base64,!!!"}]}`,
		`{"images":[{"file_id":"file-abc"}]}`,
		`{"images":[{"image_url":"file:///etc/passwd"}]}`,
		`{"images":[{"image_url":"https://a.example"},{"image_url":"https://b.example"}]}`,
		`{"images":[{"image_url":"data:image/png;base64,aGVsbG8="}]}`,
	} {
		if _, _, err := Convert([]byte(body), "application/json", nil); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for i := 0; i < 2; i++ {
		p, _ := w.CreateFormFile("image[]", fmt.Sprint(i)+".png")
		_, _ = p.Write(pngBytes(t))
	}
	_ = w.Close()
	if _, _, err := Convert(b.Bytes(), w.FormDataContentType(), nil); err == nil {
		t.Fatal("multiple images accepted in single-image mode")
	}
	if _, _, err := Convert([]byte("x"), "multipart/form-data; boundary=abc", nil); err == nil {
		t.Fatal("malformed form accepted")
	}
}

func TestScopeRequiresAccountHostAndEndpoint(t *testing.T) {
	for _, tc := range []struct {
		base, path string
		id         int64
		ids        string
		want       bool
	}{
		{"https://rpm.tosky.io/v1", "/v1/images/edits", 27, "27", true},
		{"https://rpm.tosky.io", "/v1/images/edits", 27, "20, 27", true},
		{"https://rpm.tosky.io", "/v1/images/edits", 27, "", false},
		{"https://rpm.tosky.io", "/v1/images/edits", 27, "20", false},
		{"https://rpm.tosky.io", "/v1/images/generations", 27, "27", false},
		{"https://api.openai.com", "/v1/images/edits", 27, "27", false},
		{"https://rpm.tosky.io.evil.example", "/v1/images/edits", 27, "27", false},
	} {
		if got := Enabled(tc.base, tc.path, tc.id, tc.ids); got != tc.want {
			t.Errorf("Enabled(%+v)=%v", tc, got)
		}
	}
}
