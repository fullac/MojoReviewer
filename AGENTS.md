# MojoReviewer

GitHub webhook 进来，只读审查一个 PR，用 `gh` 把结论文字发回去。进程内嵌 LuckyAgent，不另起 `lh serve`。

## 边界

- 只审查。不改被审仓库，不 `git commit`，不 `git push`，不 `gh pr create`，不 `gh pr merge`。
- 模型工具必须落在这次检出里。比较基线是 `refs/review/base`，不是本地 `main`。
- 检出是 detached、可能 shallow。禁止 `git diff main...HEAD` 和任何假定本地有 base 分支的三点 diff。
- 一个进程只用 `agent.provider` 和 `agent.model` 这一组。不要按任务换模型。
- 审查串行，队列 32。满了 webhook 回 429。不要改成无界队列。
- 同一个 PR 的同一个 head SHA 只自动审一次。带 @触发用户 的评论可以再审，并续用已有会话。
- 自己发出的评论以 `【MojoReviewer】` 开头。解析 webhook 时要跳过，避免自己触发自己。
- 有 `review_note` 时，发出去的正文用笔记拼。不要改回直接发模型自由文本。

## 布局

```
cmd/mojoreviewer/main.go     读配置、把 token 放进 GH_TOKEN、启动 runner 和 HTTP
internal/server/server.go    POST /webhook/github、GET /health、单个 worker
internal/github/event.go     pull_request 与 issue_comment 解析，HMAC 校验
internal/review/workspace.go 克隆、fetch、把 base 固定到 refs/review/base
internal/review/diff.go      提示词里的整份 diff，上限 120_000 字节
internal/review/tools.go     检出内的只读工具
internal/review/notes.go     严重 / 建议笔记，渲染最终评论
internal/review/comment.go   gh pr comment
internal/agent/runner.go     sdk.New、会话、提示词
internal/agent/register.go   每次审查重新绑定那 7 个工具
internal/store/store.go      data_dir/reviews.json，记 head SHA 和 session ID
internal/config/config.go    .MojoReviewer/config.json
```

模块是 `github.com/mojoreviewer/mojoreviewer`。LuckyAgent 用 `replace` 指到 `../luckyagent`。

## 一次审查

1. `github.ValidSignature`。secret 为空直接拒绝。body 最多读 2 MiB。
2. `pull_request` 只收 `opened`、`reopened`、`synchronize`，草稿丢弃。`issue_comment` 只收 PR 上新建的、提到 `github.trigger_user` 的评论。
3. `github.repos` 非空时，仓库不在名单里就忽略。
4. `review.PrepareWithBase` 把 head 检出到 `data_dir/workspaces/<sessionKey>`，并把 base SHA 抓到 `refs/review/base`。
5. `review.Diff` 生成提示词里的 diff。超长就截断，后面的文件靠 `pr_file_diff` 补。
6. `runner.Review` 绑定工具、跑一轮。新 PR 开新会话，评论触发复用 `reviews.json` 里的 session。
7. `review.PostComment` 发回 PR。

## 模型工具

都在 `internal/agent/register.go` 注册，每次审查先卸再挂，闭包只指向当前检出。

| 工具 | 作用 |
| --- | --- |
| `pr_diff_stat` | 文件列表、增删行数、整份 diff 是否被截断 |
| `pr_file_diff` | 单个文件相对 `refs/review/base` 的 patch |
| `read_repo_file` | 检出内读文件，可带行号 |
| `repo_grep` | 只在检出内搜索 |
| `changed_symbols` | 改动的函数、类型、方法。Go 用 `go/parser`，其他语言用声明行正则 |
| `tests_around` | 改动文件附近已有的测试 |
| `review_note` | 记一条。`severity` 只能是 `严重` 或 `建议` |

`read_repo_file` 和 `pr_file_diff` 拒绝 `..` 和检出外绝对路径。加新工具时保持这个范围，不要给模型写文件、访问网络、或调用桌面控制。`runner.go` 里已禁用的内置工具不要重新打开。

## 配置与数据

配置是仓库里的 `.MojoReviewer/config.json`，已在 `.gitignore`。不要提交，不要把 `api_key`、`webhook_secret`、`token` 写进代码、日志、测试或这份文件。

缺 `agent.api_key` 或 `github.webhook_secret` 时进程拒绝启动。`github.token` 在 `GH_TOKEN` 为空时写入环境，给 `git` 和 `gh` 用。

`data_dir` 默认 `~/.mojoreviewer`。LuckyAgent 家目录是其中的 `luckyagent/`，不读写 `~/.luckyagent`。审查记录在 `reviews.json`，权限 `0600`。

监听默认 `127.0.0.1:9968`。本机入口是 `go run ./cmd/mojoreviewer`。

## 验证

```bash
go test ./...
go vet ./...
```

改 diff、路径范围或 webhook 解析时，补对应包的测试。涉及检出的测试用 `t.TempDir()` 里的本地 git 仓库，不要打到 GitHub。

## 改动时不要做的事

- 不要把审查改成多 worker，除非同时处理会话和检出目录的并发。
- 不要在提示词里再塞一整份未截断 diff 来绕过工具。
- 不要假定被审仓库里存在名为 `main` 的本地分支。
- 不要把 `.codex-checkpoint.md`、`bin/`、`*.db`、`.env` 加进提交。
