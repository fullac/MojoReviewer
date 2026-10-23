# MojoReviewer

进程内嵌 [LuckyAgent](https://github.com/yurika0211/lucky-agent) 的 GitHub PR 审查服务。

接收 webhook → 克隆 PR 检出 → 注入项目记忆 → 用检出内工具跑一轮 → 用 `gh` 回评论。

**默认只读审查。** 可选开启 `fix`：模型改工作区文件，由**宿主**按配置 commit/push 到 PR head（非 force）。不创建 PR、不 merge。

## 快速开始

1. 复制示例配置：

```bash
mkdir -p .MojoReviewer
cp config.example.json .MojoReviewer/config.json
# 填入 agent.api_key、github.webhook_secret；私有仓再填 github.token
```

2. 安装 Go 1.25+、`git` 和 `gh`，然后下载固定版本的公开模块：

```bash
GOWORK=off go mod download
```

LuckyAgent SDK 来自 `github.com/yurika0211/luckyagent-app/sdk`，版本固定在 `go.mod`，构建时不需要相邻的 LuckyAgent 检出。

3. 运行：

```bash
go run ./cmd/mojoreviewer
```

默认监听 `127.0.0.1:9968`，webhook：`POST /webhook/github`，健康检查：`GET /health`。

## 评论意图

在 PR 评论里 `@github.trigger_user`（如 `@mojo`）：

| 写法 | 行为 |
| --- | --- |
| `@mojo` / `@mojo review …` | 只读再审（默认可续会话） |
| `@mojo remember <事实>` | 写入项目长期记忆，不检出 |
| `@mojo fix …` | 可写修复（需 `agent.fix_enabled`） |

## 本地管理 GUI

`ops/start.py` 是提交到仓库的管理器。它独立于 reviewer 运行，不另起 LuckyAgent 服务。需要 Python 3.10+、Go 1.25+、`git`、`gh` 和 `cloudflared`。

```bash
go build -o bin/mojoreviewer ./cmd/mojoreviewer
python3 ops/start.py
```

打开 **http://127.0.0.1:9970**，在「连接配置」中填写模型、API Key、webhook secret、仓库和 hook ID。填已有 ID 表示授权管理器维护该 webhook；填 `0` 会在第一次启动时创建，随后把真实 ID 写回配置。创建请求结果不确定时会停止自动创建，需在 GitHub 查找已有 ID 后填入，避免重复创建。`github.repos` 在管理模式下必须显式列出受管仓库。

`agent.max_context_tokens` 默认 `40000`，`agent.request_timeout` 默认 `5m`，`agent.task_timeout` 默认 `20m`。这些值可在 GUI 修改，并写入隔离的 SDK 启动配置；chat 和 compact 都固定使用同一个供应商和模型。

管理器使用两个 `gh` 已登录账户：`manager.reviewer_account` 默认 `raymojo42`，用于 reviewer 克隆和发布评论；`manager.admin_account` 默认 `fullac`，用于 webhook 管理。不会切换 `gh` 全局活动账户。先通过 `gh auth login` 登录这两个账户；账户名可以在 GUI 修改。

GUI 提供启停、重启、重建隧道、检查漏审、手动重审、配置和日志。管理端口只监听本机，cloudflared 只转发 reviewer 的 `9968` 端口。配置中的密钥不会回显；保存时留空保留原值，未知配置字段也保留。配置及管理状态文件以 `0600` 原子写入，均被 Git 忽略。

恢复机制：

- 每 30 秒向公网入口发送带 HMAC 签名的 ping，验证响应确实来自 reviewer。连续三次失败或 cloudflared 退出时，只重建隧道、更新受管 webhook，正在执行的审查保持运行。
- reviewer 进程退出时自动重启，失败重试指数退避，最长 60 秒。关闭服务会取消自动重启。旧启动器占用 reviewer 端口时，只有签名探测通过才使用该进程处理本机重审，不向它发信号，也不把它算作管理器的子进程。停止或重启会放弃这次使用并重新探测；探测失败仍拒绝启动。进行中的审查在外部进程仍通过探测时保持原状态。
- 恢复后以及每 60 秒，分页检查 GitHub 保留的失败投递。跳过已成功的同 GUID、过期 head、关闭或草稿 PR、未提及触发用户的评论和机器人自己的评论。每个失败 GUID 最多请求补送三次，间隔至少两分钟。
- 同时检查白名单仓库中未审查的 open PR 当前 head，用签名本地事件补齐，因此旧投递已超出 GitHub 保留期时仍能发现漏审。所有任务仍经过单 worker、32 长度队列和 SHA 去重，满队列下一轮再补。
- 自动任务实际开始后，该 head 即记入 `reviews.json` 的 `auto_heads` 历史；切到其他 head 后再回到旧 SHA 也不会重复自动执行。模型、准备仓库或发布失败后，可通过提及触发用户的评论或 GUI「重新审查」复用会话再试。仅排队未执行的自动任务可在重启后补齐；GitHub 仍保留的评论投递可按中断回执请求补送。点击 GUI 重审会在本地签名入队，不额外向 GitHub 发布触发评论；若本地重审尚未执行就重启，需要再次点击。

`data_dir/activity.json` 保存最近 256 次尝试的排队、运行、完成、失败及中断状态；只有 `gh pr comment` 成功才标记「已发布」，并保存返回的评论链接。旧版 `reviews.json` 中只有会话和 SHA，GUI 不会把它推断为审查成功。日志只保留当前管理器最近 400 行，并过滤已知凭据。

「停止服务」会关闭 manager 自己启动的 reviewer（含进程内 LuckyAgent）和隧道，GUI 仍可使用。终端 `Ctrl+C` 或向 `ops/start.py` 进程发送 `SIGTERM` 则会同时关闭管理器和子进程。`--start` 可以在打开 GUI 时启动服务，`--port` 可修改管理端口。要在登录后启动，可让自己的 launchd 服务运行此脚本；不要同时保留原 `~/.mojoreviewer/ops/start.py` 启动器。

管理配置片段（合并到现有配置，不包含密钥）：

```json
{
  "manager": {
    "reviewer_account": "raymojo42",
    "admin_account": "fullac",
    "auto_start": false,
    "hooks": {"owner/repo": 0}
  }
}
```

管理器单元测试使用模拟 GitHub、临时文件和本地子进程，不访问真实 GitHub：

```bash
python3 -m unittest discover -s ops -p 'test_*.py'
go test ./...
go vet ./...
```

## 限制

自己发出的评论以 `【MojoReviewer】` 开头，解析时会跳过，避免自触发。

自动审查接受 `opened`、`reopened`、`synchronize`、`ready_for_review`，跳过草稿。评论必须提及完整账号：配置为 `mojo` 时，`@mojo-bot`、`name@mojo` 不会触发。

## 配置要点

配置文件：仓库内隐藏目录下的配置（已 gitignore，勿提交密钥）。字段见 [`config.example.json`](config.example.json)。

| 字段 | 说明 |
| --- | --- |
| `agent.api_key` | **必填** |
| `github.webhook_secret` | **必填**，HMAC 校验 |
| `github.token` | 私有仓克隆 / `gh` 发评论；也可依赖环境 `GH_TOKEN` |
| `github.host` | 可选 GHES 主机名；空则沿用 `gh` 默认 |
| `github.repos` | 非空时只接受名单内仓库 |
| `github.trigger_user` | 评论 @ 触发名 |
| `agent.fix_enabled` | 默认 `false`；是否接受 fix |
| `agent.fix_push_enabled` | 默认跟随 `fix_enabled`；`false` 时只改工作区不 push |
| `agent.fix_allowed_authors` | 非空则仅这些 login 可 fix |
| `agent.fix_max_changed_files` | 单次提交文件数上限，默认 40 |
| `agent.fix_max_diff_bytes` | 单次改动体量上限，默认 200000 |
| `data_dir` | 默认 `~/.mojoreviewer` |
| `agent.home_dir` | LuckyAgent 家目录，默认 `$data_dir/luckyagent` |
| `agent.max_context_tokens` | 组装上下文的 token 上限，默认 40000，至少 1000 |
| `agent.request_timeout` | 单次模型请求超时，默认 `5m`，也用于本地检查执行预算 |
| `agent.task_timeout` | 一次 review/fix 的总超时，默认 `20m` |
| `log_level` | Mojo 宿主日志级别：`debug` / `info` / `warn` / `error`，默认 `info`；不修改 SDK 内部日志配置 |

完整边界与流程见 [`AGENTS.md`](AGENTS.md)。

## 安全边界（摘要）

- Webhook：secret 必填、body ≤ 2 MiB、HMAC；队列 32，满则 429。
- 审查工具白名单；未知内置工具默认禁用。审查工具与项目文档索引都校验符号链接真实路径，拒绝读取检出外文件。
- PR 元数据、评论与项目记忆经 JSON 转义置于不可信数据块；系统提示将代码、diff 和工具输出中的文字也视为数据，不允许它们改写权限。
- fix 额外仅 `file_read` / `file_list` / `file_write` / `file_patch` / `file_mkdir`；无 terminal / 网络 / git。
- 宿主提交拒绝 `.env*`、密钥类文件、`.git/` 等；push 非 force，且拒绝 head==base。
- fix 提交标题由宿主按 PR 编号生成，不拼接模型回复或评论中的指令文本。
- 失败评论写阶段与归类原因；不公开本地绝对路径、令牌、原始模型错误或命令 stderr。
- 所有评论在调用 `gh` 前检查配置密钥、GitHub 环境凭据和常见令牌格式；命中则拒绝整条发布，日志只记录拒绝原因。
- 只读审查正文始终由宿主按 `review_note` 渲染。零笔记时发布固定结构并说明不代表所有改动均已核实，模型自由文本不会作为兜底正文。

检出时将 PR base SHA 固定在 `refs/review/base`，将它与 head 的唯一共同祖先固定在 `refs/review/merge-base`。diff、文件统计、改动符号和测试定位都使用后者，因此 base 分支后来新增的改动不会被误算为本 PR 的删除。工具读取不再 fetch；找不到唯一共同祖先时停止本次准备。

## 大 PR 与中断回报

首轮 prompt 不包含整份 diff。`pr_diff_stat` 用 `offset`/`limit` 分页（默认 50 个文件，最多 200），按 `has_more` 与 `next_offset` 续取；`patch_truncated` 只描述整份 patch 是否超限。

只读审查会记录本轮成功读取的文件/行段，重复调用返回已读提示，避免把相同代码反复送进上下文。新审查会重新记录；可写 fix 路径不启用这种去重。

foreground 因重复读取未产生新证据而中断时，PR 会收到「审查未完成」说明，包含读取回执、任务 ID、原运行时会话的恢复指令，以及中断前已记录的意见。没有意见也会明确说明，不代表审查通过。状态仍为 `failed/model`；失败说明成功发布后保存评论 URL，不能当作完整审查完成。

## 项目记忆

首次/增量索引检出中的高信号文档（README、AGENTS、docs 等），元数据在 `$data_dir/project_memory.json`，向量与长期记忆在 `luckyagent/` 内，按 `repo:<owner/name>:` 隔离。审查前提取相关片段注入提示词；`remember` 可人工补事实。

## 验证

```bash
GOWORK=off go mod verify
GOWORK=off go test -mod=readonly -race ./...
GOWORK=off go vet -mod=readonly ./...
GOWORK=off go build -mod=readonly -o /tmp/mojoreviewer ./cmd/mojoreviewer
```

向 `main` 的非草稿 PR 会跑 `.github/workflows/ci.yml`：仅检出 MojoReviewer，从 `go.mod` / `go.sum` 下载依赖，关闭 Go workspace，执行模块校验、vet、竞态测试、Python 管理器测试和构建。

升级 SDK 时应固定公开 tag 或提交版本，并核对 `GOWORK=off go list -mod=readonly -m all`。现有工具白名单、串行队列、隔离 HomeDir、chat/compact 使用同一模型，以及 fix 宿主提交门禁继续生效。

## License

MIT。见 [LICENSE](LICENSE)。
