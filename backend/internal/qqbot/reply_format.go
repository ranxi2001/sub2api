package qqbot

import (
	"regexp"
	"strings"
)

var replyHTMLBreak = regexp.MustCompile(`(?i)<\s*br\s*/?\s*>[ \t]*\r?\n?`)

// normalizeReplyLineBreaks accepts common HTML line-breaks from model and
// administrator-authored text, then sends native newlines to QQ Markdown.
func normalizeReplyLineBreaks(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	// Leave code examples intact, including HTML tags inside backtick spans
	// and fenced blocks. Only presentation markup outside code is normalized.
	var out strings.Builder
	for len(text) > 0 {
		start := strings.IndexAny(text, "`~")
		if start < 0 {
			_, _ = out.WriteString(replyHTMLBreak.ReplaceAllString(text, "\n"))
			break
		}
		_, _ = out.WriteString(replyHTMLBreak.ReplaceAllString(text[:start], "\n"))
		text = text[start:]
		n := 1
		for n < len(text) && text[n] == text[0] {
			n++
		}
		marker := text[:n]
		if text[0] == '~' && n < 3 {
			_, _ = out.WriteString(marker)
			text = text[n:]
			continue
		}
		end := strings.Index(text[n:], marker)
		if end < 0 {
			if n >= 3 { // An unfinished code block is still code.
				_, _ = out.WriteString(text)
				break
			}
			_, _ = out.WriteString(marker)
			text = text[n:]
			continue
		}
		end += 2 * n
		_, _ = out.WriteString(text[:end])
		text = text[end:]
	}
	return out.String()
}

var replyLabelEscapes = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "~", `\~`,
	"[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`, "$", `\$`,
)

// Operator-defined names are labels, not Markdown templates.
func replyPlainLabel(text string) string {
	return replyLabelEscapes.Replace(strings.Join(strings.Fields(text), " "))
}

// QQ allows five passive messages per incoming message. Split the reply within
// the remaining slots, accounting for any messages already sent.
func splitReply(text string, maxChunks int) []string {
	if maxChunks <= 0 {
		return nil
	}
	r := []rune(text)
	const width = 1500
	if len(r) > width*maxChunks {
		r = append(r[:width*maxChunks-50], []rune("\n（内容过长，已截断。）")...)
	}
	var chunks []string
	for len(r) > 0 {
		n := min(len(r), width)
		// Keep normal list entries and paragraphs intact when possible.
		if n < len(r) {
			// A shorter chunk must still leave enough room in the remaining
			// passive replies. Otherwise a newline-heavy response can exceed
			// QQ's five-message limit even after the initial truncation.
			minWidth := max(width/2, len(r)-width*(maxChunks-len(chunks)-1))
			for i := n - 1; i+1 >= minWidth; i-- {
				if r[i] == '\n' {
					n = i + 1
					break
				}
			}
		}
		chunks = append(chunks, string(r[:n]))
		r = r[n:]
	}
	if len(chunks) == 0 {
		return []string{"暂无内容。"}
	}
	return chunks
}
