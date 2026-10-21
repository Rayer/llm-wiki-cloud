package urlintake

import (
	"strings"
)

// NormalizeMarkdown corrects only structural escapes produced by the existing
// Fetch provider and removes a short allowlist of recognizable page chrome.
func NormalizeMarkdown(markdown string) string {
	lines := strings.SplitAfter(markdown, "\n")
	var out strings.Builder
	var fence markdownFence
	for _, line := range lines {
		content := strings.TrimSuffix(line, "\n")
		if fence.char != 0 {
			out.WriteString(line)
			if closesMarkdownFence(content, fence) {
				fence = markdownFence{}
			}
			continue
		}
		if leadingIndent(content) >= 4 {
			out.WriteString(line)
			continue
		}
		if opened, ok := openMarkdownFence(content); ok {
			fence = opened
			out.WriteString(line)
			continue
		}
		trimmed := strings.TrimSpace(content)
		if isProviderChrome(trimmed) {
			continue
		}
		out.WriteString(unescapeStructuralPrefix(content))
		if strings.HasSuffix(line, "\n") {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

type markdownFence struct {
	char   byte
	length int
}

func openMarkdownFence(line string) (markdownFence, bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) || (line[indent] != '`' && line[indent] != '~') {
		return markdownFence{}, false
	}
	char := line[indent]
	end := indent
	for end < len(line) && line[end] == char {
		end++
	}
	length := end - indent
	if length < 3 || (char == '`' && strings.Contains(line[end:], "`")) {
		return markdownFence{}, false
	}
	return markdownFence{char: char, length: length}, true
}

func closesMarkdownFence(line string, fence markdownFence) bool {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) || line[indent] != fence.char {
		return false
	}
	end := indent
	for end < len(line) && line[end] == fence.char {
		end++
	}
	return end-indent >= fence.length && strings.TrimSpace(line[end:]) == ""
}

func isProviderChrome(line string) bool {
	switch line {
	case "Skip to content", "給個讚", "本文重點｜快速連結", "訂閱網站推播通知", "已阻擋通知",
		"We'd like to show you notifications for the latest news and updates\\.",
		"We\\'d like to show you notifications for the latest news and updates\\.":
		return true
	default:
		return false
	}
}

func leadingIndent(line string) int {
	count := 0
	for _, r := range line {
		if r == ' ' {
			count++
			continue
		}
		if r == '\t' {
			count += 4
			continue
		}
		break
	}
	return count
}

func unescapeStructuralPrefix(line string) string {
	trim := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(trim)]
	if len(trim) < 2 || trim[0] != '\\' {
		return line
	}
	marker := trim[1]
	if marker != '#' && marker != '-' && marker != '*' && marker != '+' {
		return line
	}
	index := 0
	for index+1 < len(trim) && trim[index] == '\\' && trim[index+1] == marker {
		index += 2
	}
	if marker == '#' && (index >= len(trim) || trim[index] != ' ') {
		return line
	}
	if (marker == '-' || marker == '*' || marker == '+') && (index >= len(trim) || trim[index] != ' ') {
		return line
	}
	return indent + strings.Repeat(string(marker), index/2) + trim[index:]
}
