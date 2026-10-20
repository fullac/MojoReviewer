package github

import (
	"strings"
	"unicode"
)

// Intent 是评论里解析出的操作意图。
type Intent string

const (
	IntentReview   Intent = "review"
	IntentFix      Intent = "fix"
	IntentRemember Intent = "remember"
)

// ParseIntent 从评论正文解析意图和剩余指令。
// 未写出命令时，只要提到了触发用户，默认按 review。
func ParseIntent(comment, triggerUser string) (Intent, string) {
	text := strings.TrimSpace(comment)
	if text == "" {
		return IntentReview, ""
	}
	triggerUser = strings.TrimPrefix(strings.TrimSpace(triggerUser), "@")

	// 去掉所有 @trigger 提及，方便取命令词。
	if triggerUser != "" {
		for {
			idx, end := mentionRange(text, triggerUser)
			if idx < 0 {
				break
			}
			text = strings.TrimSpace(text[:idx] + text[end:])
		}
	}

	text = strings.TrimSpace(text)
	text = strings.TrimLeft(text, ":：,，;-－")
	text = strings.TrimSpace(text)
	if text == "" {
		return IntentReview, ""
	}

	cmd, rest := splitCommand(text)
	switch normalizeCommand(cmd) {
	case "fix", "repair", "patch", "修复", "修改", "改":
		return IntentFix, rest
	case "remember", "memo", "note", "记住", "记下", "记忆":
		return IntentRemember, rest
	case "review", "recheck", "look", "审查", "重审", "再审", "看看", "看下":
		return IntentReview, rest
	default:
		// 没有显式命令：整段当作 review 补充说明。
		return IntentReview, text
	}
}

func splitCommand(text string) (cmd, rest string) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || r == ':' || r == '：' || r == ',' || r == '，'
	})
	if len(fields) == 0 {
		return "", ""
	}
	cmd = fields[0]
	// 用原串切掉首词，保留后文空白形态的简化版。
	idx := strings.Index(text, cmd)
	if idx < 0 {
		return cmd, ""
	}
	rest = strings.TrimSpace(text[idx+len(cmd):])
	rest = strings.TrimLeft(rest, ":：,，;-－")
	rest = strings.TrimSpace(rest)
	return cmd, rest
}

func normalizeCommand(cmd string) string {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	cmd = strings.Trim(cmd, ":：,，;/\\")
	return cmd
}
