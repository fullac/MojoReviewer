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

## 限制

整个进程只用配置里的一组 `agent.provider` 和 `agent.model`，不能按单次任务换模型。审查前会把会话工作目录设到这次检出。模型另外还有一组只在检出内可用的工具：`pr_diff_stat`、`pr_file_diff`、`read_repo_file`、`repo_grep`、`changed_symbols`、`tests_around`、`review_note`。比较基线是 `refs/review/base`。
