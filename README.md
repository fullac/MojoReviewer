# MojoReviewer

用 LuckyAgent 公开 SDK 做 GitHub Pull Request 只读审查。

它接收 GitHub webhook，把 PR 克隆到本机，再把仓库路径、截断后的 diff 和一组检出内只读工具交给进程内的 LuckyAgent。服务再用 `gh` 把结论文字发回 PR。它不修改文件，不提交，不推送，不创建或合并 PR。

## 运行

配置写在项目里的 `.MojoReviewer/config.json`。先填 `agent.api_key` 和 `github.webhook_secret`。`github.host` 可选，填 GitHub Enterprise 的主机名。补齐 PR 信息和发布评论都会用它；留空则沿用 `gh` 已配置的主机。`github.token` 用来克隆私有仓库和发布评论。`github.repos` 为空时接受所有仓库。`github.trigger_user` 填了之后，评论里 @ 这个名字会再审一次。

```bash
go run ./cmd/mojoreviewer
```

监听地址来自 `server.host` 和 `server.port`，默认是 `127.0.0.1:9968`。webhook 地址是 `POST /webhook/github`。同一个 PR 的同一个提交只审一次；评论触发不受这个限制。审查一次只跑一个，队列最多 32 个。

评论触发的任务开始执行时，会用 `gh api` 查询 PR 的最新 head SHA、base ref/SHA 和仓库克隆地址，再准备检出和 diff，并复用已有会话。查询 PR 信息或准备仓库失败时，会把归类后的原因作为 `【MojoReviewer】` 评论发回 PR，不附带命令输出、本地路径或令牌；完整错误只写日志。评论发布失败也只记录日志。

`data_dir` 为空时数据放在 `~/.mojoreviewer`。LuckyAgent 使用其中单独的 `luckyagent` 目录，不读写 `~/.luckyagent`。`agent.home_dir` 可以改掉这个位置。

## 本地管理 GUI

`ops/start.py` 是提交到仓库的管理器。它独立于 reviewer 运行，不另起 LuckyAgent 服务。需要 Python 3.10+、`gh` 和 `cloudflared`，并在相邻的 `../luckyagent` 准备 SDK。

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
- reviewer 进程退出时自动重启，失败重试指数退避，最长 60 秒。关闭服务会取消自动重启。旧启动器占用 reviewer 端口时拒绝启动，不接管其进程。
- 恢复后以及每 60 秒，分页检查 GitHub 保留的失败投递。跳过已成功的同 GUID、过期 head、关闭或草稿 PR、未提及触发用户的评论和机器人自己的评论。每个失败 GUID 最多请求补送三次，间隔至少两分钟。
- 同时检查白名单仓库中未审查的 open PR 当前 head，用签名本地事件补齐，因此旧投递已超出 GitHub 保留期时仍能发现漏审。所有任务仍经过单 worker、32 长度队列和 SHA 去重，满队列下一轮再补。
- 自动任务实际开始后，该 head 即记为已尝试。模型、准备仓库或发布失败不会每分钟重复执行；GUI 的「重新审查」可复用会话再试。仅排队而未执行的任务在进程重启后可由补齐检查恢复。点击重审会在本地签名入队，不额外向 GitHub 发布触发评论。

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

整个进程只用配置里的一组 `agent.provider` 和 `agent.model`，不能按单次任务换模型。审查前会把会话工作目录设到这次检出。模型另外还有一组只在检出内可用的工具：`pr_diff_stat`、`pr_file_diff`、`read_repo_file`、`repo_grep`、`changed_symbols`、`tests_around`、`review_note`。比较基线是 `refs/review/base`。

## CI

向 `main` 提交或更新非草稿 Pull Request 时，GitHub Actions 会跑 `go vet`、`go test` 和 `go build`。工作流会额外检出公开的 `yurika0211/lucky-agent` 到相邻的 `luckyagent/`，以匹配本地 `replace => ../luckyagent`。

## License

MIT。见 [LICENSE](LICENSE)。
