// Test the publishing template boundary, including Markdown escape attempts.
package review

import (
	"strings"
	"testing"
)

func TestNoteBodyTemplateBoundary(t *testing.T) {
	valid := "- **问题：** 错误分支没有返回。\n\n- **影响：** 后续仍会执行。\n\n- **建议修复：** 返回错误。"
	for _, tc := range []struct{ name, body string }{
		{"free text", "发现一个问题"},
		{"missing item", strings.Replace(valid, "- **影响：** 后续仍会执行。\n\n", "", 1)},
		{"empty item", strings.Replace(valid, "后续仍会执行。", "", 1)},
		{"wrong order", strings.Replace(valid, "**影响：**", "**建议修复：**", 1)},
		{"wrong severity", strings.Replace(valid, "**问题：**", "**现状：**", 1)},
		{"duplicate", valid + "\n\n- **问题：** 重复"},
		{"supplement", valid + "\n\n- **补充说明：** 多余内容"},
		{"verification", valid + "\n\n- **验证建议：** 多余内容"},
		{"position", valid + "\n\n📍 a.go:1"},
		{"nested item", valid + "\n\n  - **补充说明：** 多余内容"},
		{"nested heading", valid + "\n\n  ### 验证"},
		{"nested label", valid + "\n\n  **代码位置：** a.go"},
		{"plain label", valid + "\n\n  补充说明：多余内容"},
		{"unindented continuation", valid + "\n\n额外内容"},
		{"html", valid + "\n\n  <h3>额外内容</h3>"},
		{"no blank line", strings.ReplaceAll(valid, "\n\n", "\n")},
		{"missing language", valid + "\n\n  ```\n  return err\n  ```"},
		{"unterminated code", valid + "\n\n  ```go\n  return err"},
		{"code outside last item", strings.Replace(valid, "后续仍会执行。", "后续仍会执行。\n\n  ```go\n  return err\n  ```", 1)},
		{"unindented code", valid + "\n\n  ```go\nreturn err\n  ```"},
		{"longer fence escape", valid + "\n\n  ```go\n  ````\n  - **补充说明：** 多余内容\n  ```"},
		{"whitespace fence escape", valid + "\n\n  ```go\n  ``` \n  - **补充说明：** 多余内容\n  ```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var notes Notes
			if err := notes.Add("严重", "a.go", 1, tc.body); err == nil || notes.Len() != 0 {
				t.Fatalf("invalid note reached storage: %v", err)
			}
		})
	}
	for _, code := range []string{
		"", "\n\n  *仅调整错误分支。*",
		"\n\n  ```go\n  if err != nil {\n      return err\n  }\n  ```",
		"\n\n  ````markdown\n  - **验证建议：** 这是代码内容，不是额外栏目。\n  ```go\n  return err\n  ```\n  ````",
	} {
		var notes Notes
		if err := notes.Add("严重", "a.go", 1, valid+code); err != nil {
			t.Fatalf("valid Markdown rejected: %v", err)
		}
		if got := notes.Render(); !strings.Contains(got, valid+code) || strings.Contains(got, "📍") || strings.Contains(got, "a.go:1") {
			t.Fatalf("rendered body or location boundary changed: %s", got)
		}
	}
}
