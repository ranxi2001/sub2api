package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyGeminiChatResponseFormat 把 Chat Completions 的 response_format 写入 Gemini generationConfig：
// json_object 写 responseMimeType=application/json；json_schema 另把 json_schema.schema 原样写入
// responseJsonSchema（JSON Schema 形态，不转换为 OpenAPI 子集的 responseSchema）。
// text、null 和缺省不改请求；无效格式、缺少 schema 或当前兼容路径不支持的组合
// 返回错误，由调用方在发送上游请求之前响应 400，不能静默丢弃约束。
func applyGeminiChatResponseFormat(geminiReq []byte, responseFormat json.RawMessage, upstreamModel string) ([]byte, error) {
	if len(responseFormat) == 0 {
		return geminiReq, nil
	}
	if !gjson.ValidBytes(responseFormat) {
		return nil, errors.New("response_format must be a valid JSON object")
	}
	format := gjson.ParseBytes(responseFormat)
	if format.Type == gjson.Null {
		return geminiReq, nil
	}
	if !format.IsObject() {
		return nil, errors.New("response_format must be a JSON object")
	}

	var schema gjson.Result
	switch format.Get("type").String() {
	case "text":
		return geminiReq, nil
	case "json_object":
	case "json_schema":
		schema = format.Get("json_schema.schema")
		if !schema.IsObject() {
			return nil, errors.New("response_format.json_schema.schema must be a JSON object")
		}
	default:
		return nil, errors.New("response_format.type must be text, json_object or json_schema")
	}

	if isGeminiImageOutputModel(upstreamModel) {
		return nil, errors.New("JSON response_format is not supported for Gemini image output models")
	}

	if tools := gjson.GetBytes(geminiReq, "tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return nil, errors.New("JSON response_format with tools is not supported by this Gemini compatibility path")
	}

	out, err := sjson.SetBytes(geminiReq, "generationConfig.responseMimeType", "application/json")
	if err != nil {
		return nil, fmt.Errorf("encode Gemini response MIME type: %w", err)
	}
	if schema.IsObject() {
		withSchema, err := sjson.SetRawBytes(out, "generationConfig.responseJsonSchema", []byte(schema.Raw))
		if err != nil {
			return nil, fmt.Errorf("encode Gemini response JSON schema: %w", err)
		}
		out = withSchema
	}
	return out, nil
}
