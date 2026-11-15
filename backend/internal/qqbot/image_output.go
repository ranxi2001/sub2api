package qqbot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/gif"
	"net/http"
	"strings"
)

// Keep the existing QQ upload ceiling. Never silently flatten an animation to
// fit it. Bound decoded work before DecodeAll allocates every frame.
const maxGeneratedImageBytes = 3 << 20
const maxGIFFrames = 200
const maxGIFPixels = 80_000_000

type imageOutputError string

func (e imageOutputError) Error() string { return string(e) }

func validImageOutputFormat(format string) bool {
	switch format {
	case "", "png", "jpeg", "webp", "gif":
		return true
	default:
		return false
	}
}

func imageOutputConfig(c Config, format string) Config {
	if format != "" && format != "auto" {
		c.ImageOutputFormat = format
	}
	return c
}

// Explicit command options only; ordinary chat chooses its format via the tool
// schema, never by matching words in an untrusted prompt.
func imageCommandOptions(c Config, prompt string) (Config, string) {
	words := strings.Fields(prompt)
	if len(words) == 0 {
		return c, prompt
	}
	format := ""
	switch words[0] {
	case "--gif":
		format = "gif"
	case "--static":
		format = "png"
	default:
		return c, prompt
	}
	return imageOutputConfig(c, format), strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(prompt), words[0]))
}

func generatedImageType(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return http.DetectContentType(data)
}

func generatedImageFileName(data []byte) string {
	if generatedImageType(data) == "image/gif" {
		return "ctav.gif"
	}
	return "ctav.jpg"
}

func normalizeGeneratedImage(data []byte) ([]byte, error) {
	if generatedImageType(data) != "image/gif" {
		return normalizeImage(data)
	}
	if len(data) > maxGeneratedImageBytes {
		return nil, imageOutputError("GIF 超过机器人 3 MB 上传限制，请减少帧数或画面尺寸后重试；未转成静态图。")
	}
	if err := validateGIFBudget(data); err != nil {
		return nil, err
	}
	if _, err := gif.DecodeAll(bytes.NewReader(data)); err != nil {
		return nil, imageOutputError("GIF 动画内容损坏，无法发送。")
	}
	// Preserve frame pixels, timing, transparency, disposal and loop count exactly.
	return data, nil
}

func checkRequestedImageFormat(data []byte, format string) ([]byte, error) {
	if format == "gif" && generatedImageType(data) != "image/gif" {
		return nil, imageOutputError("生图渠道没有返回 GIF，请检查模型、渠道及 GIF 权限；未将静态图冒充动画。")
	}
	return data, nil
}

// Walk block lengths without decompressing pixels. DecodeAll subsequently
// validates palette indexes, LZW data and GIF semantics within these bounds.
func validateGIFBudget(data []byte) error {
	invalid := imageOutputError("GIF 动画内容损坏，无法发送。")
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return invalid
	}
	width, height := int(binary.LittleEndian.Uint16(data[6:8])), int(binary.LittleEndian.Uint16(data[8:10]))
	if width < 1 || height < 1 || width > 2048 || height > 2048 {
		return imageOutputError("GIF 宽高须在 1～2048 像素之间，请减小画面尺寸；未转成静态图。")
	}
	i := 13
	if data[10]&0x80 != 0 {
		i += 3 * (1 << ((data[10] & 7) + 1))
	}
	blocks := func() bool {
		for i < len(data) {
			n := int(data[i])
			i++
			if n == 0 {
				return true
			}
			if n > len(data)-i {
				return false
			}
			i += n
		}
		return false
	}
	frames := 0
	var pixels int64
	for i < len(data) {
		kind := data[i]
		i++
		switch kind {
		case 0x3b:
			if frames == 0 || i != len(data) {
				return invalid
			}
			return nil
		case 0x21:
			if i >= len(data) {
				return invalid
			}
			i++ // extension label
			if !blocks() {
				return invalid
			}
		case 0x2c:
			if len(data)-i < 9 {
				return invalid
			}
			x, y := int(binary.LittleEndian.Uint16(data[i:i+2])), int(binary.LittleEndian.Uint16(data[i+2:i+4]))
			w, h := int(binary.LittleEndian.Uint16(data[i+4:i+6])), int(binary.LittleEndian.Uint16(data[i+6:i+8]))
			packed := data[i+8]
			i += 9
			if w < 1 || h < 1 || x+w > width || y+h > height {
				return invalid
			}
			frames++
			pixels += int64(w) * int64(h)
			if frames > maxGIFFrames || pixels > maxGIFPixels {
				return imageOutputError("GIF 帧数或总像素超过机器人处理限制，请减少帧数或画面尺寸；未转成静态图。")
			}
			if packed&0x80 != 0 {
				i += 3 * (1 << ((packed & 7) + 1))
			}
			if i >= len(data) {
				return invalid
			}
			i++ // LZW minimum code size
			if !blocks() {
				return invalid
			}
		default:
			return invalid
		}
	}
	return invalid
}

func imageOutputMessage(err error) string {
	var outputErr imageOutputError
	if errors.As(err, &outputErr) {
		return outputErr.Error()
	}
	return ""
}
