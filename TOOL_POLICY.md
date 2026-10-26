<!-- Purpose: record the fixed tool-set contract and its cross-repository validation boundary. -->
# 按模式固定工具集合

核对日期：2026-10-27（Asia/Shanghai）。

## 行为

工具实现仍在 Mojo，注册表与模型循环仍由 LH 管理，不新增 Gateway。
`Runner.Review` 和 `Runner.Fix` 把各自的白名单交给 `runModel`，由它设置
`LoopConfig.RestrictTools=true` 和排序后的 `AllowedTools`。

- review：`pr_diff_stat`、`pr_file_diff`、`read_repo_file`、`repo_grep`、
  `changed_symbols`、`tests_around`、`review_note`。
- fix：上述七个，加上 `file_read`、`file_list`、`file_write`、`file_patch`、`file_mkdir`。
- 显式集合跳过提示词工具筛选；原有禁用、隐藏和沙箱限制优先。
- 模型只能执行本轮集合内的工具。提示词指导任务步骤，不决定可用集合。
- LH 为整个集合保留 schema 预算；任务与工具无法一起装入请求时，在模型调用前失败。
- 普通循环仍最多 30 轮，不更换模型，不新增 foreground 检查点。

## 提交与依赖边界

| 仓库 | 分支 | 状态 |
| --- | --- | --- |
| lucky-agent | `fix/host-tool-policy` | 本地提交 `36652db4b88e4d1f247e346c15a5c14fb45c3156`，基于公开主线 `5873586` |
| MojoReviewer | `fix/mode-tool-policy` | 宿主接入与请求级回归测试 |

LH 改动尚未发布。Mojo 的 `go.mod/go.sum` 仍固定原来的公开版本，不能把
默认依赖构建视为该功能已接入。旧 LH 的 `AllowedTools` 只是后置过滤，
`TestModeToolPolicyIndependentOfPrompt` 在该版本上会失败。

本地验证使用仓库外临时 `go.work`，只选择 Mojo 与上述 LH 的 `agent/` 模块，
没有在任何提交中加入本机路径或 `replace`。联测选中的公开组件为：

- app：`v0.0.0-20261023123441-69866b490ce5`。
- runtime：`v0.3.1-0.20261025155307-54def1957a16`。
- tools：`v0.2.4-0.20261025155312-14ca32a46742`。

## 验证

- LH 新增契约测试（含 race）：通过。覆盖措辞变化、意图筛选开关、禁用/隐藏工具、
  空集合、旧调用方式兼容、同步/流式预算、真实循环与复用会话。
- LH Agent `go vet -mod=readonly ./...`：通过。
- LH Agent 全量 race：使用 `TMPDIR=/private/tmp` 后只剩
  `TestBuildSystemPromptIncludesLowercaseAgentsContext` 失败；未修改的 `5873586`
  基线也在该测试失败。默认 macOS 临时目录另触发插件符号链接和图片路径限制，
  本次未修改这些无关测试或限制。
- LH 根 launcher：使用其可移植 `go.work` 的测试、构建通过，包含本地 Agent。
- Mojo 临时联合 workspace：全量 race、vet、构建及 19 项 Python 管理器测试通过。
  请求检查覆盖 review 的 7 个工具、fix 的 12 个工具、后续轮次、提示词变化、
  PR 评论中的工具指令，以及同一会话 review → fix → review 的模式切换。
- 未推送、未创建 PR、未部署，未进行真实 PR 审查验收。

后续按依赖顺序发布 LH 提交，再更新 Mojo 的公开模块版本，关闭 workspace 复跑
`GOWORK=off` 的完整检查与模块版本核对。全部通过后才能移除此分支的依赖待发布提示。
