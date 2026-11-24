package service

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

// sanitizeOpenAIPassthroughClientErrorText 脱敏透传错误字段，除常规敏感参数外，
// 若账号配置了自定义 base_url 或凭据（api_key/access_token），一并屏蔽，
// 避免上游错误文案泄露真实上游地址或账号凭据。
func sanitizeOpenAIPassthroughClientErrorText(account *Account, text string) string {
	// Recognize encrypted diagnostics before any configured literal can hide
	// their marker. Redact secrets before query sanitization changes their bytes.
	text = redactOpenAIEncryptedDiagnostic(strings.TrimSpace(text))
	return sanitizeUpstreamErrorMessage(redactOpenAIPassthroughAccountSecrets(account, text))
}

// Keep account-secret redaction separate from diagnostic redaction: even an
// otherwise valid protocol identifier can be a configured header secret.
func redactOpenAIPassthroughAccountSecrets(account *Account, sanitized string) string {
	if sanitized == "" || account == nil {
		return sanitized
	}
	var secrets []string
	for _, credKey := range []string{"api_key", "access_token", "refresh_token"} {
		if secret := strings.TrimSpace(account.GetCredential(credKey)); len(secret) >= 6 {
			secrets = append(secrets, secret)
		}
	}
	for _, template := range account.GetHeaderOverrides() {
		// Use the same parser as trusted identity headers, without resolving or
		// recording request identity. Short separators must not corrupt all text.
		walkHeaderOverrideTemplate(template, func(literal string) {
			literal = strings.TrimSpace(literal)
			if len(literal) >= 6 && strings.ContainsFunc(literal, func(r rune) bool {
				return unicode.IsLetter(r) || unicode.IsDigit(r)
			}) {
				secrets = append(secrets, literal)
			}
		}, func(string) bool { return true })
	}
	// Match overlapping values longest first, without rescanning replacement text.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	pairs := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		pairs = append(pairs, secret, "[redacted]")
	}
	if len(pairs) > 0 {
		sanitized = strings.NewReplacer(pairs...).Replace(sanitized)
	}
	rawBaseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if rawBaseURL == "" {
		return sanitized
	}
	parsed, err := url.Parse(rawBaseURL)
	if err != nil {
		return sanitized
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return sanitized
	}
	return replaceCaseInsensitive(sanitized, host, "[upstream]")
}

// replaceCaseInsensitive 把 input 中所有大小写不敏感匹配 target 的片段替换为
// replacement。
//
// 必须直接在原始字节上匹配：strings.ToLower 会改变字节长度（"Ⱥ" 2→3 字节、
// Kelvin 符号 "K" 3→1 字节、非法 UTF-8 单字节→3 字节 U+FFFD），用小写串的下标
// 去切原串会越界 panic 或漏掉 target。正则的 (?i) 在原串上逐 rune 折叠匹配，
// 不存在下标错位。
//
// target 无法编译为模式（如含非法 UTF-8）时返回空串：调用方会回退到通用文案，
// 宁可丢弃整段文本，也不把未脱敏的内容写给客户端。
func replaceCaseInsensitive(input, target, replacement string) string {
	if input == "" || target == "" {
		return input
	}
	pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(target))
	if err != nil {
		return ""
	}
	return pattern.ReplaceAllLiteralString(input, replacement)
}

// openAIPassthroughClientErrorFieldMaxBytes 限制透出的 error.type/code/param
// 单字段长度。三者都是短标识符，超长只可能是上游在夹带其它内容。
const openAIPassthroughClientErrorFieldMaxBytes = 256

// openAIPassthroughClientErrorField 提取允许写给客户端的标量错误字段。
// gjson 对对象/数组的 String() 返回整段原始 JSON，直接透出等于把上游任意
// 扩展字段塞进 type/code/param，因此只接受字符串与数字（部分上游的 code 为
// 数字），并在脱敏后做长度上限校验，超限即丢弃。
func openAIPassthroughClientErrorField(account *Account, value gjson.Result) string {
	if value.Type != gjson.String && value.Type != gjson.Number {
		return ""
	}
	field := sanitizeOpenAIPassthroughClientErrorText(account, value.String())
	if len(field) > openAIPassthroughClientErrorFieldMaxBytes {
		return ""
	}
	return field
}

// openAIPassthroughClientErrorCode 返回允许写给客户端的 error.code。
// error.code 缺失、为 null 或为空白字符串时，沿用 extractUpstreamErrorCode 对
// "message 内嵌 JSON"形态的兼容提取（与错误分类看到的 code 保持一致）；
// error.code 是对象、数组或布尔值时不回退，直接视为无 code。
func openAIPassthroughClientErrorCode(account *Account, body []byte) string {
	code := gjson.GetBytes(body, "error.code")
	// These protocol identifiers carry no diagnostic text or encrypted locator.
	trimmed := strings.TrimSpace(code.Str)
	if code.Type == gjson.String && (trimmed == "invalid_encrypted_content" || trimmed == "thinking_signature_invalid") {
		return redactOpenAIPassthroughAccountSecrets(account, trimmed)
	}
	blank := !code.Exists() || code.Type == gjson.Null ||
		(code.Type == gjson.String && strings.TrimSpace(code.Str) == "")
	if !blank {
		return openAIPassthroughClientErrorField(account, code)
	}
	inner := extractUpstreamErrorCode(body)
	// Apply the same exact protocol-code exception to the classifier's nested
	// message fallback. Diagnostic text and every other field stay sanitized.
	if inner == "invalid_encrypted_content" || inner == "thinking_signature_invalid" {
		return redactOpenAIPassthroughAccountSecrets(account, inner)
	}
	inner = sanitizeOpenAIPassthroughClientErrorText(account, inner)
	if len(inner) > openAIPassthroughClientErrorFieldMaxBytes || strings.HasPrefix(inner, "{") || strings.HasPrefix(inner, "[") {
		return ""
	}
	return inner
}
