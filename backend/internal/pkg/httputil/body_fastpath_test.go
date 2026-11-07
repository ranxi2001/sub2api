package httputil

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
)

// Frozen pre-experiment behavior for local differential checks.
func legacyNormalize(body []byte, maxNormalizedBytes int64) ([]byte, error) {
	if maxNormalizedBytes <= 0 {
		maxNormalizedBytes = maxDecompressedBodySize
	}
	body = trimUTF8BOM(body)
	if len(body) == 0 {
		return body, nil
	}
	if int64(len(body)) > maxNormalizedBytes {
		return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
	}
	var out []byte
	inString := false
	escaped := false
	for i, b := range body {
		if inString && isJSONControlByte(b) {
			if out == nil {
				capHint := len(body) + 6
				if int64(capHint) > maxNormalizedBytes {
					capHint = int(maxNormalizedBytes)
				}
				out = make([]byte, 0, capHint)
				out = append(out, body[:i]...)
			}
			if int64(len(out)+6) > maxNormalizedBytes {
				return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
			}
			out = appendJSONUnicodeEscape(out, b)
			escaped = false
			continue
		}
		switch {
		case escaped:
			escaped = false
		case inString && b == '\\':
			escaped = true
		case b == '"':
			inString = !inString
		}
		if out != nil {
			if int64(len(out)+1) > maxNormalizedBytes {
				return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
			}
			out = append(out, b)
		}
	}
	if out != nil {
		return out, nil
	}
	return body, nil
}

func assertSameNormalization(t testing.TB, body []byte, limit int64) {
	t.Helper()
	want, wantErr := legacyNormalize(body, limit)
	got, gotErr := NormalizeLenientJSONRequestBody(body, limit)
	if (want == nil) != (got == nil) || !bytes.Equal(want, got) {
		t.Fatalf("normalization differs: size=%d limit=%d", len(body), limit)
	}
	var wantMax, gotMax *http.MaxBytesError
	if errors.As(wantErr, &wantMax) != errors.As(gotErr, &gotMax) {
		t.Fatalf("error type differs: old=%v new=%v", wantErr, gotErr)
	}
	if wantMax != nil && wantMax.Limit != gotMax.Limit {
		t.Fatalf("error limit differs: old=%d new=%d", wantMax.Limit, gotMax.Limit)
	}
	if (wantErr == nil) != (gotErr == nil) {
		t.Fatalf("error presence differs: old=%v new=%v", wantErr, gotErr)
	}
}

func TestControlScanExhaustivePairs(t *testing.T) {
	for offset := 0; offset < 8; offset++ {
		for first := 0; first < 256; first++ {
			for second := 0; second < 256; second++ {
				body := bytes.Repeat([]byte{'a'}, 24)
				body[offset], body[offset+1] = byte(first), byte(second)
				want := isJSONControlByte(byte(first)) || isJSONControlByte(byte(second))
				if got := hasJSONControlBytes(body); got != want {
					t.Fatalf("scan differs at offset=%d values=%d,%d", offset, first, second)
				}
			}
		}
	}
}

func TestNormalizeLargeAllBytesAndLimits(t *testing.T) {
	for _, size := range []int{65535, 65536, 65537, 65543} {
		body := bytes.Repeat([]byte{'x'}, size)
		body[0], body[len(body)-1] = '"', '"'
		for _, limit := range []int64{0, int64(size - 1), int64(size), int64(size + 6)} {
			assertSameNormalization(t, body, limit)
		}
	}
	for _, position := range []int{1, 7, 8, 9, 32768, 65534} {
		for value := 0; value < 256; value++ {
			body := bytes.Repeat([]byte{'x'}, 65536)
			body[0], body[len(body)-1] = '"', '"'
			body[position] = byte(value)
			assertSameNormalization(t, body, int64(len(body)+6))
		}
	}
	for _, raw := range [][]byte{
		nil, {}, []byte("\\\"\\\n"), []byte("\xef\xbb\xbf{}"),
		append([]byte("\xef\xbb\xbf{\"input\":\""), bytes.Repeat([]byte{'x'}, 65536)...),
	} {
		assertSameNormalization(t, raw, 0)
	}
}

func FuzzControlScan(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("abcdef"), {0}, {127}, {128, 255}, bytes.Repeat([]byte{'x'}, 17)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		want := false
		for _, b := range body {
			if isJSONControlByte(b) {
				want = true
				break
			}
		}
		if hasJSONControlBytes(body) != want {
			t.Fatalf("control-byte scan differs for %d bytes", len(body))
		}
	})
}

func FuzzNormalizeLargeParity(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("abcdef"), {0}, {127}, []byte(`\\\"`), {128, 255}} {
		f.Add(seed, uint16(7), uint8(0))
	}
	f.Fuzz(func(t *testing.T, patch []byte, offset uint16, limitChoice uint8) {
		if len(patch) > 256 {
			return
		}
		body := bytes.Repeat([]byte{'x'}, 65544)
		body[0], body[len(body)-1] = '"', '"'
		start := 1 + int(offset)%(len(body)-len(patch)-2)
		copy(body[start:], patch)
		limits := []int64{0, int64(len(body) - 1), int64(len(body)), int64(len(body) + 6), int64(len(body) + 2048)}
		assertSameNormalization(t, body, limits[int(limitChoice)%len(limits)])
	})
}

var benchmarkNormalizeSink []byte

func BenchmarkNormalizeLargeControlFree(b *testing.B) {
	body := append([]byte(`{"input":"`), bytes.Repeat([]byte("abcdefghijklmno"), 1<<19)...)
	body = append(body, []byte(`"}`)...)
	for _, tc := range []struct {
		name string
		fn   func([]byte, int64) ([]byte, error)
	}{
		{"legacy", legacyNormalize}, {"current", NormalizeLenientJSONRequestBody},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				out, err := tc.fn(body, int64(len(body)))
				if err != nil {
					b.Fatal(err)
				}
				benchmarkNormalizeSink = out
			}
		})
	}
}
