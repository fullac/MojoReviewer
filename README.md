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

2. 本地依赖：相邻目录有 `luckyagent`（`go.mod` 里 `replace => ../luckyagent`），本机有 `git` / `gh`。

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

自己发出的评论以 `【MojoReviewer】` 开头，解析时会跳过，避免自触发。

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

完整边界与流程见 [`AGENTS.md`](AGENTS.md)。

## 安全边界（摘要）

- Webhook：secret 必填、body ≤ 2 MiB、HMAC；队列 32，满则 429。
- 审查工具白名单；路径不得逃出本次检出。
- fix 额外仅 `file_read` / `file_list` / `file_write` / `file_patch` / `file_mkdir`；无 terminal / 网络 / git。
- 宿主提交拒绝 `.env*`、密钥类文件、`.git/` 等；push 非 force，且拒绝 head==base。
- 失败评论只写阶段与归类原因；路径、令牌、命令 stderr 只进日志。

## 项目记忆

首次/增量索引检出中的高信号文档（README、AGENTS、docs 等），元数据在 `$data_dir/project_memory.json`，向量与长期记忆在 `luckyagent/` 内，按 `repo:<owner/name>:` 隔离。审查前提取相关片段注入提示词；`remember` 可人工补事实。

## 验证

```bash
go test ./...
go vet ./...
go build -o /tmp/mojoreviewer ./cmd/mojoreviewer
```

向 `main` 的非草稿 PR 会跑 `.github/workflows/ci.yml`（同样三步；CI 会检出公开的 `yurika0211/lucky-agent` 到相邻 `luckyagent/`）。

## License

MIT。见 [LICENSE](LICENSE)。
