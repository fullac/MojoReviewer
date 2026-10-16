# MojoReviewer

GitHub webhook 进来，只读审查一个 PR，用 `gh` 把结论文字发回去。进程内嵌 LuckyAgent，不另起 `lh serve`。

## 边界

- 默认只审查。不 `gh pr create`，不 `gh pr merge`。
- `fix` 且 `agent.fix_enabled` 时：模型可改检出内文件；**宿主**在 `fix_push_enabled` 下 commit/push 到 PR head（非 force，有作者/路径/体量门禁）。仍禁止模型自己调用 terminal/git。
- 模型工具必须落在这次检出里。比较基线是 `refs/review/base`，不是本地 `main`。
- 检出是 detached、可能 shallow。禁止 `git diff main...HEAD` 和任何假定本地有 base 分支的三点 diff。
- 一个进程只用 `agent.provider` 和 `agent.model` 这一组。不要按任务换模型。
- 审查串行，队列 32。满了 webhook 回 429。不要改成无界队列。
- 同一个 PR 的同一个 head SHA 只自动审一次。带 @触发用户 的评论可以再审，并续用已有会话。
- 评论意图：`review`（默认）、`remember`（写入项目记忆）、`fix`（需 `agent.fix_enabled=true`；改工作区后由宿主 commit/push 到 head 分支）。
- 自己发出的评论以 `【MojoReviewer】` 开头。解析 webhook 时要跳过，避免自己触发自己。
- `github.host` 为空时，`gh api` 不传 `--hostname`，发评论也不覆盖 `GH_HOST`。有值时，查 PR 和发评论必须用同一个主机。不要再把主机写死成 `github.com`。
- 失败评论只写阶段和归类原因。`gh`/`git` 的 stderr、本地路径和令牌只进日志。
- 有 `review_note` 时，发出去的正文用笔记拼。不要改回直接发模型自由文本。

## 配置与安全

- `agent.fix_enabled`（默认 false）：是否接受 `@trigger fix`。
- `agent.fix_push_enabled`（默认跟随 `fix_enabled`）：fix 结束后是否由宿主 commit/push。可显式 `false` 只改工作区。
- `agent.fix_allowed_authors`：非空时，仅列表内 GitHub login 可触发 fix。
- `agent.fix_max_changed_files`（默认 40）、`agent.fix_max_diff_bytes`（默认 200000）：单次提交上限。
- 宿主提交拒绝敏感路径：`.env*`、密钥类扩展名（`.pem`/`.key`/`.jks` 等）、`id_rsa`、`secrets.json`、`.git/` 等。
- push **非 force**，且拒绝 head==base。
- 模型侧：review 工具白名单；fix 额外 `file_{read,list,write,patch,mkdir}`；无 terminal/网络/git。
- webhook：secret 必填；body ≤ 2 MiB；HMAC；`github.repos` 可收窄仓库。

## 布局

```
.github/workflows/ci.yml     PR 到 main 时跑 vet/test/build
LICENSE                      MIT
cmd/mojoreviewer/main.go     读配置、把 token 放进 GH_TOKEN、启动 runner 和 HTTP
internal/server/server.go    POST /webhook/github、GET /health、单个 worker
internal/github/event.go     pull_request 与 issue_comment 解析，HMAC 校验
internal/github/intent.go    评论意图：review / fix / remember
internal/review/workspace.go 克隆、fetch、把 base 固定到 refs/review/base
internal/review/publish.go   fix 结束后宿主 commit/push 到 PR head
internal/review/diff.go      可选 diff 读取（调试/测试）；审查主路径不再写入 prompt
internal/review/tools.go     检出内的只读工具
internal/review/notes.go     严重 / 建议笔记，渲染最终评论
internal/review/comment.go   gh pr comment
internal/agent/runner.go     sdk.New、会话、提示词
internal/agent/register.go   每次审查重新绑定那 7 个工具
internal/store/store.go      data_dir/reviews.json，记 head SHA 和 session ID
internal/projectmem/         项目记忆：选文档、增量索引、审查前提示注入
internal/config/config.go    .MojoReviewer/config.json
config.example.json          可提交的配置样例（无真实密钥）
```

模块是 `github.com/mojoreviewer/mojoreviewer`。LuckyAgent 用 `replace` 指到 `../luckyagent`。

## 一次审查

1. `github.ValidSignature`。secret 为空直接拒绝。body 最多读 2 MiB。
2. `pull_request` 只收 `opened`、`reopened`、`synchronize`，草稿丢弃。`issue_comment` 只收 PR 上新建的、提到 `github.trigger_user` 的评论。
3. `github.repos` 非空时，仓库不在名单里就忽略。
4. `review.PrepareWithBase` 把 head 检出到 `data_dir/workspaces/<sessionKey>`，并把 base SHA 抓到 `refs/review/base`。
5. 提示词**不**预置整份 diff（CodeFarmer 做法）。模型先 `pr_diff_stat`，再 `pr_file_diff` 按文件读。
6. `projectmem` 对检出做增量索引（README/AGENTS/docs 等），并把 Recall/RAG 片段注入提示词。索引失败只记日志，不阻断审查。
7. `runner.Review` 绑定工具、跑一轮。新 PR 开新会话，评论触发复用 `reviews.json` 里的 session。
8. `review.PostComment` 发回 PR。

## fix 路径

1. 评论解析为 `IntentFix`，且 `agent.fix_enabled`。
2. `fix_allowed_authors` 非空时校验 `item.Author`。
3. 与审查相同方式 `PrepareWithBase` + 项目记忆。
4. `runner.Fix`：fix 工具白名单 + 会话 cwd 钉在检出。
5. `review.PublishFix`：敏感路径 / 体量门禁 → 可选 commit/push 到 head。
6. 评论回报 fix 结果与 publish 摘要；成功 push 时更新 `reviews.json` 的 head SHA。

## 模型工具

都在 `internal/agent/register.go` 注册，每次审查先卸再挂，闭包只指向当前检出。

| 工具 | 作用 |
| --- | --- |
| `pr_diff_stat` | 文件列表分页（offset/limit）、增删行数、整份 patch 是否超限 |
| `pr_file_diff` | 单个文件相对 `refs/review/base` 的 patch |
| `read_repo_file` | 检出内读文件，可带行号 |
| `repo_grep` | 只在检出内搜索 |
| `changed_symbols` | 改动的函数、类型、方法。Go 用 `go/parser`，其他语言用声明行正则 |
| `tests_around` | 改动文件附近已有的测试 |
| `review_note` | 记一条。`severity` 只能是 `严重` 或 `建议` |

`pr_diff_stat` 默认每页 50 个文件，最多 200；`has_more=true` 时按 `next_offset` 续取，直到 `has_more=false`。`patch_truncated` 只表示整份 patch 超限，不能当作文件地图不完整。

只读审查每轮维护独立的读取回执。同一文件的 patch、同一标准化行段重复调用时只返回已读提示，不再加载原文；新一轮重新建立回执。fix 路径可写文件，因此不复用去重记录。证据充分后记录 `review_note` 并收束，不要循环重读。

模型中断时保留已记录的笔记和读取范围，发布带「审查未完成」的失败说明；可识别的 foreground 任务只公开任务 ID 与原运行时恢复指令，不公开原始错误/prompt。失败说明发布后仍记录 `failed/model`，只补充评论 URL，不能标记为审查完成。

`read_repo_file` 和 `pr_file_diff` 拒绝 `..` 和检出外绝对路径。加新工具时保持这个范围，不要给模型写文件、访问网络、或调用桌面控制。

工具边界由 `runner.go` 的 allow-list 强制：

1. `sdk.New` 时用 `bootstrapDisableTools` 先关掉明显危险的内置工具。
2. `registerReviewTools` 挂上这 7 个审查工具。
3. `lockToolsToReviewOnly` 再把 **不在** `reviewToolAllowlist` 里的全部工具 `DisableTool`。
4. `fix` 且 `agent.fix_enabled` 时走 `lockToolsToFixMode`：在审查工具之外额外启用 `file_read` / `file_list` / `file_write` / `file_patch` / `file_mkdir`。
5. 会话 cwd 通过 `sdk.Agent.SetSessionWorkingDir` 钉到本次检出。

不要重新 `EnableTool` 打开 `terminal` / 网络 / 桌面控制类工具。新增审查工具时，同步更新 `registerReviewTools` 与对应 allow-list。
`fix` 结束后宿主调用 `review.PublishFix`：校验敏感路径与体量后，按 `fix_push_enabled` commit/push 到 `HeadBranch`（拒绝推 base、不做 force）。

## 配置与数据

配置是仓库里的 `.MojoReviewer/config.json`，已在 `.gitignore`。不要提交，不要把 `api_key`、`webhook_secret`、`token` 写进代码、日志、测试或这份文件。

缺 `agent.api_key` 或 `github.webhook_secret` 时进程拒绝启动。`github.token` 在 `GH_TOKEN` 为空时写入环境，给 `git` 和 `gh` 用。

`data_dir` 默认 `~/.mojoreviewer`。LuckyAgent 家目录是其中的 `luckyagent/`，不读写 `~/.luckyagent`。审查记录在 `reviews.json`，项目记忆元数据在 `project_memory.json`，权限都是 `0600`。RAG 与长期记忆落在 `luckyagent/` 内，按仓库隔离 source 前缀 `repo:<owner/name>:`。

监听默认 `127.0.0.1:9968`。本机入口是 `go run ./cmd/mojoreviewer`。

## 验证

```bash
go test ./...
go vet ./...
go build -o /tmp/mojoreviewer ./cmd/mojoreviewer
```

向 `main` 开非草稿 PR 会触发 `.github/workflows/ci.yml` 做同样检查。CI 会把 `yurika0211/lucky-agent` 检到仓库旁的 `luckyagent/`，对齐 `go.mod` 的 `replace`。

改 diff、路径范围或 webhook 解析时，补对应包的测试。涉及检出的测试用 `t.TempDir()` 里的本地 git 仓库，不要打到 GitHub。

## 改动时不要做的事

- 不要把审查改成多 worker，除非同时处理会话和检出目录的并发。
- 不要在提示词里再塞整份 diff 来绕过工具。首轮 prompt 只给元数据 + 步骤；diff 一律工具按需读（issue #21）。\n- durable/foreground 会把 goal pin 住并在 history 再留一份，有效预算远小于 `max_context_tokens`；靠调大 token 上限不能代替控制 prompt 体积。
- 不要假定被审仓库里存在名为 `main` 的本地分支。
- 不要把 `.codex-checkpoint.md`、`bin/`、`*.db`、`.env` 加进提交。
