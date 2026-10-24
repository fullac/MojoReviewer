// Enforce Mojo's comment template before any model-generated note is stored.
package review

import (
	"fmt"
	"regexp"
	"strings"
)

// NoteBodyRules is shared by the system prompt and review_note tool schema.
const NoteBodyRules = "正文必须恰好三项 Markdown 无序列表，按顺序且每项非空：严重问题使用「问题」「影响」「建议修复」；建议使用「现状」「收益」「建议调整」。每项严格写成 - **标签：** 内容，项间留空行。禁止增加、重复或改名栏目，禁止补充说明、验证、代码依据、代码位置等独立栏目，以及列表外文字、小标题、嵌套列表或 HTML。续行必须缩进两个空格。必要代码、命令、配置或日志片段只能放在最后的建议修复/建议调整项内，使用带语言标记的围栏代码块，围栏与内容额外缩进两个空格。短标识符使用行内代码，适当加粗、斜体；不重复标题、分组或数量，不单独展示文件位置。"

var noteCodeFence = regexp.MustCompile("^  (`{3,}|~{3,})([A-Za-z][A-Za-z0-9_+.-]*)$")
var noteExtraStructure = regexp.MustCompile(`^(?:📍|(?:补充说明|验证(?:建议|方法|步骤)?|代码(?:依据|位置))[：:]|#{1,6}(?:\s|$)|[-+*]\s|[0-9]+[.)]\s|[>|]|[-_*]{3,}$)|\*\*[^*\n]+[：:]\*\*|</?[A-Za-z!]|!\[`)

func validateNoteBody(severity, body string) error {
	labels := []string{"问题", "影响", "建议修复"}
	if severity == "建议" {
		labels = []string{"现状", "收益", "建议调整"}
	}
	invalid := func() error {
		return fmt.Errorf("body 模板不符：必须依次且仅包含 %s 三项非空列表，项间空行；续行缩进两个空格，带语言的代码块仅放最后一项。禁止额外栏目、标题、位置或列表外文字", strings.Join(labels, "、"))
	}
	lines := strings.Split(body, "\n")
	section := -1
	fence := ""
	for i, line := range lines {
		if fence != "" {
			if line == "  "+fence {
				fence = ""
			} else if trimmed := strings.TrimSpace(line); len(trimmed) >= len(fence) && strings.Trim(trimmed, fence[:1]) == "" {
				// Markdown also closes fences with extra markers or whitespace.
				// Require the canonical form so following text cannot escape validation.
				return invalid()
			} else if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
				return invalid()
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "- **") {
			section++
			if section >= len(labels) || (i > 0 && strings.TrimSpace(lines[i-1]) != "") {
				return invalid()
			}
			prefix := "- **" + labels[section] + "：** "
			if !strings.HasPrefix(line, prefix) {
				return invalid()
			}
			content := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if content == "" || noteExtraStructure.MatchString(content) || strings.Contains(content, "```") || strings.Contains(content, "~~~") {
				return invalid()
			}
			continue
		}
		if section < 0 || !strings.HasPrefix(line, "  ") {
			return invalid()
		}
		if match := noteCodeFence.FindStringSubmatch(line); match != nil {
			if section != 2 || i == 0 || strings.TrimSpace(lines[i-1]) != "" {
				return invalid()
			}
			fence = match[1]
			continue
		}
		content := strings.TrimSpace(line)
		if strings.HasPrefix(line, "    ") || strings.Contains(line, "\t") || noteExtraStructure.MatchString(content) || strings.Contains(content, "```") || strings.Contains(content, "~~~") {
			return invalid()
		}
	}
	if section != 2 || fence != "" {
		return invalid()
	}
	return nil
}
