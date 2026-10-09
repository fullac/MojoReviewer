# MojoReviewer

用 LuckyAgent 公开 SDK 做 GitHub Pull Request 只读审查。

它接收 GitHub webhook，把 PR 克隆到本机，再把仓库绝对路径和 diff 交给进程内的 LuckyAgent。模型只能读仓库、跑带 `workdir` 的终端命令。服务再用 `gh` 把结论文字发回 PR。它不修改文件，不提交，不推送，不创建或合并 PR。

## 运行

配置写在项目里的 `.MojoReviewer/config.json`。先填 `agent.api_key` 和 `github.webhook_secret`。`github.token` 用来克隆私有仓库和发布评论。`github.repos` 为空时接受所有仓库。`github.trigger_user` 填了之后，评论里 @ 这个名字会再审一次。

```bash
go run ./cmd/mojoreviewer
```

监听地址来自 `server.host` 和 `server.port`，默认是 `127.0.0.1:9968`。webhook 地址是 `POST /webhook/github`。同一个 PR 的同一个提交只审一次；评论触发不受这个限制。审查一次只跑一个，队列最多 32 个。

`data_dir` 为空时数据放在 `~/.mojoreviewer`。LuckyAgent 使用其中单独的 `luckyagent` 目录，不读写 `~/.luckyagent`。`agent.home_dir` 可以改掉这个位置。

## 限制

LuckyAgent 公开 SDK 不能给会话设置工作目录，也不能按单次任务换模型。所以每个命令都要在提示词里写绝对路径，整个进程只用配置里的一组 `agent.provider` 和 `agent.model`。
