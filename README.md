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

`agent.max_context_tokens` 默认 `40000`，`agent.request_timeout` 默认 `5m`，`agent.task_timeout` 默认 `20m`。这些值可在 GUI 修改，并写入隔离的 SDK 启动配置；chat 和 compact 都固定使用同一个供应商和模型，温度固定为 `0.5`。

Mojo 在隔离的 SDK 配置中关闭 `agent.foreground.enabled`，使用最多 30 次迭代的普通工具循环，避免前台完成检查在空验收条件下反复续跑。宿主通过现有公开 `RunLoopWithSession` 接口获取执行结果，避免 `ChatSession` 在循环失败后回退到普通聊天而掩盖错误。任务总时限和结果发布由 Mojo 管理。会话仍可复用，但新任务不生成 LH foreground 检查点，也不支持从该检查点续跑。只修改 Mojo 配置和宿主逻辑，不修改 LH 源码。

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

## 评论格式

普通审查由宿主汇总 `review_note`：先显示数量摘要，再按「🔴 严重问题」「🟡 建议」分组，空分组省略。每条只显示加粗编号标题和固定三项正文；代码位置仅放在正文对应列表项中，与 path/line 一致，不再另加 📍 位置段落。没有笔记时明确说明不代表所有改动均已核实。

系统提示和工具说明共用一份模板规则，宿主在记录笔记前校验正文：严重问题和建议都必须依次包含「问题描述、代码位置、修复建议」，三项均不得为空。额外、重复、乱序栏目、独立标题、嵌套列表、HTML、列表外文字和额外位置段落会被拒绝并反馈给模型修正，不会原样发布或静默丢弃多余内容。

各项使用 `- **标签：** 内容`，项间空行，续行缩进两个空格。适当使用加粗、斜体，短标识符用行内代码。必要代码片段、命令、配置和日志仅放在最后的修复建议项内，使用带语言标记的围栏代码块，围栏和内容额外缩进两个空格。结构校验不代表意见内容、代码证据或测试结果已核实。

两类意见统一使用以下正文格式：

````markdown
- **问题描述：** 保存失败后接口仍返回成功，重启后任务可能丢失。

- **代码位置：** `internal/server/server.go:123`

- **修复建议：** 保存失败时返回错误。建议修改示例：

  ```go
  if err != nil {
      return err
  }
  ```
````

## 限制

自己发出的评论以 `【MOJORviewer🥕】` 开头，解析时会跳过新前缀及旧前缀 `【MojoReviewer】`，避免自触发。

自动审查接受 `opened`、`reopened`、`synchronize`、`ready_for_review`，跳过草稿。评论必须提及完整账号：配置为 `mojo` 时，`@mojo-bot`、`name@mojo` 不会触发。

任务真正开始处理时，Mojo 会添加 👀 reaction：自动审查加在 PR 正文，`@` 触发加在原评论；排队中、被忽略或去重的事件不会提前添加。GUI 本地重审没有原评论，因此跳过 reaction。同一账号已有的 👀 会复用，不代表本轮已完成；最终结果以审查评论为准。reaction 请求最多等待 5 秒，失败只记日志，继续审查。使用细粒度令牌时需授予 Issues 写权限。

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

检出时将 PR base SHA 固定在 `refs/worktree/review/base`，将它与 head 的唯一共同祖先固定在 `refs/worktree/review/merge-base`。diff、文件统计、改动符号和测试定位都使用后者，因此 base 分支后来新增的改动不会被误算为本 PR 的删除。工具读取不再 fetch；找不到唯一共同祖先时停止本次准备。

## 大 PR 与中断回报

首轮 prompt 不包含整份 diff。`pr_diff_stat` 用 `offset`/`limit` 分页（默认 50 个文件，最多 200），按 `has_more` 与 `next_offset` 续取；`patch_truncated` 只描述整份 patch 是否超限。

只读审查会记录本轮成功读取的文件/行段，重复调用返回已读提示，避免把相同代码反复送进上下文。新审查会重新记录；可写 fix 路径不启用这种去重。

重复调用保护、迭代上限或超时中断审查时，PR 会收到「审查未完成」说明，包含读取回执和中断前已记录的意见。普通循环的重复保护即使返回空错误，宿主也会识别其停止文案并按失败处理。没有意见也会明确说明，不代表审查通过。状态仍为 `failed/model`；失败说明成功发布后保存评论 URL，不能当作完整审查完成。可通过评论或 GUI 重新发起审查并复用会话；新任务没有 foreground 任务 ID 或检查点恢复指令。

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

## 审查运行观测（#52）

每次尝试结束时，`activity.json` 的 `metrics` 保存总耗时、已执行阶段耗时、模型迭代次数、接口报告的输入/输出/缓存 token 用量及最多 400 条工具轨迹。轨迹仅含已知工具名、耗时、结果字节数，不含参数、正文和原始错误；总调用次数不因截断丢失。token 是模型接口报告的文本用量单位；未报告用量显示“未记录”，不推算为零。

管理台“审查记录 → 运行观测”可展开各阶段和调用序列，成功、失败结果及服务重启后均可查看。阶段包含 checkout（检出）、memory（项目记忆）、manifest（改动清单）、model（模型）、ground（依据校验）、publish（发布）；尚未执行的阶段显示“未执行”。当前记录为尝试结束时的快照，不是逐 token 实时流。#44 的进度事件仍为独立提案，本改动不使用其 `progress/` 文件，合并时保留结构化用量和事件的各自职责。

## 固定提交回放（#53）

`cmd/mojoeval` 复用检出和审查入口，使用独立临时运行时、全新会话和本地裸仓库；不启动服务、不发评论、不执行修复。模型请求仍会发往你配置的模型接口，代码会像正常审查一样进入模型上下文。运行结束删除临时检出和会话，报告文件权限为 `0600`。

样本文件是 JSON 数组，每项包含 `id`、`repository`（本地裸仓库，相对路径以样本文件所在目录为准）、`base_sha`、`head_sha`（完整且存在于本地的提交 SHA）、可选 `title`、`expected`。每条标注包含 `path`、`start_line`、`end_line`，行号针对 head，闭区间。无问题样本必须显式写 `expected: []`；省略标注会拒绝运行。

```json
[{"id":"sample","repository":"./sample.git","base_sha":"替换为完整固定提交SHA","head_sha":"替换为完整固定提交SHA","title":"检查错误处理","expected":[{"path":"service.go","start_line":20,"end_line":25}]}]
```

```bash
GOWORK=off go run ./cmd/mojoeval -cases ./cases.json -config .MojoReviewer/config.json -output ./baseline.json
```

配置只需正常的 `agent` 模型字段，不需要 webhook secret；可通过 `MOJO_EVAL_API_KEY` 注入模型密钥。生产数据目录及 fix 开关会被覆盖为隔离配置。输出文件参数必填，运行时日志不会混进 JSON 报告。

报告按样本记录阶段耗时、迭代、token、工具轨迹，以及位置匹配的准确率（precision）、召回率（recall）和二者调和平均值（F1）。匹配要求文件相同、行号落在标注范围内，采用最大一对一匹配，重复发现计入误报。“幻觉率”是未匹配发现占全部发现的比例，属于位置匹配代理指标，不证明语义正确或虚构。分母为零的指标记零，结合原始命中/误报/漏报数量解读。失败样本保留观测并单独计数，不混入成功样本的指标；有失败时退出码为 1，配置/输入错误为 2。

保留同一套人工标注与模型配置，对不同提交分别生成报告即可比较改造前后结果。测试用本地假模型覆盖真实运行时回放，不使用付费接口。

## 宿主改动清单（#46）

只读审查在检出后预计算 `change_manifest`：文件路径、增删行、启发式风险分、改动符号和附近测试。清单按风险分及路径稳定排序，最多 200 个文件、JSON 总体积 16 KiB、单文件最多 8 个符号和 8 个测试；准备预算为 5 秒。超大源码、二进制、删除文件和无法安全读取的链接只保留文件元数据，标记 `details_partial`。风险分只控制阅读顺序，测试名称不证明测试覆盖。

清单不含补丁或源码正文，与其他 PR 数据一起通过 JSON 转义进入不可信数据块。`complete` 只描述文件列表完整性；`omitted_files` 显示缺失数量，缺失时用原有分页工具补齐；详情缺失用符号、文件和测试工具按需读取。准备失败退回原有工具流程，不把清单失败当成没有改动。运行观测会记录 manifest 阶段耗时。

回放默认启用清单，增加 `-manifest=false` 可对同一批样本生成不预置清单的对照报告。

## 项目记忆准备与缓存（#54）

宿主在检出及改动清单之后调用记忆准备，再启动模型审查或修复。记忆索引与召回共用独立的 30 秒预算；索引失败只记录分类日志，不消耗模型的 `task_timeout`，也不阻断后续审查。管理台观测项显示 memory 耗时、提交缓存是否命中及已收录文档数。

成功索引按仓库和 head 缓存，缓存包含版本号；旧元数据会重新检查一次。新 head 用文件内容哈希比较变化，避免仅凭大小与修改时间漏掉编辑。索引失败或元数据落盘失败均不记成功 head，允许下次重试；元数据使用原子写入。相同提交只跳过索引，仍为当前评论重新召回。

召回查询增加清单中的改动路径和符号，限制总长度并保持中文 UTF-8 完整，不放完整 PR 正文和补丁。文档检索结果还按本仓库的 source 前缀过滤；长期记忆沿用现有召回机制，不宣称具有文档 source 级别的仓库隔离。

回放增加 `-memory=true`，仅使用本次临时目录的记忆，不读取生产记忆。默认关闭；报告写入 manifest/memory 开关，便于比较启用与关闭的影响。启用清单与记忆的完整回放：

```bash
GOWORK=off go run ./cmd/mojoeval -cases ./cases.json -output ./with-memory.json -manifest=true -memory=true
```

## 发布前依据校验（#48）

只读审查正常结束或中断后，宿主先核验结构化意见再渲染。位置必须属于本次改动文件，行号落在改动行段或工具实际返回的源码行段内，正文位置必须与 path/line 完全一致。删除文件使用共同祖先的行号；其他文件使用 head 行号。问题描述中行内代码形式的标识符必须在该文件存在，修复建议允许新名称。越界、链接、超大或无法核验的内容不发布，评论说明过滤数量，管理台显示依据校验耗时及过滤数。此校验只确认可定位性，不证明业务判断正确；不增加额外模型复核。

## 大 PR 分批（#47）

宿主从完整 Git 统计生成批次，不依赖可能截断的提示词清单。超过 20 个文件或 2000 行增删时按目录切分，每批最多 20 个文件、目标 2000 行；单个超大文件独占一批。各批串行、独立会话，仍使用同一模型与最多 30 次迭代。整个 Review 共用 task_timeout，按剩余批次数分配剩余预算；每批重置读取回执，允许为核实调用关系读取批次外代码。正常结束的批次也不等于所有代码已核实。宿主去重汇总笔记，任一批失败时整体仍为未完成，并列出未完成文件；管理台显示完成批次数。修复路径不分批。

## 自动任务合并（#49）

同一 PR 尚未执行的自动事件共用一个队列位置，新投递替换旧载荷；旧回执记为 skipped/superseded，新旧回执一次原子落盘。评论与 GUI 显式重审独立排队，不被合并。开始前宿主查询 GitHub 当前 head；已过时任务不检出、不调用模型、不添加开始表情，也不占用已开始 head 历史。乱序事件造成最新任务被替换时，可在有空位且未重复的前提下补排远端当前 head。队列仍为 32，无法新增位置返回 429；已开始的失败尝试仍持久去重。

## 仓库对象缓存与独立检出（#50）

`data_dir/repos/` 按仓库和来源地址保存共享裸仓库，每次只请求固定 base/head，使用 blob:none 部分获取（远端不支持时 Git 可退回完整对象）。`workspaces/` 中各 PR 使用独立 detached worktree；路径带身份哈希避免清洗后的名称碰撞。比较引用使用 Git 的工作目录私有命名空间 `refs/worktree/review/base` 与 `refs/worktree/review/merge-base`，不会因其他 PR 更新而改变。准备阶段预取固定差异所需对象，模型工具不负责 fetch。

旧版独立克隆目录原样保留，新目录不会覆盖它们。复用检出若存在未提交修改，宿主保留现场并拒绝继续；准备失败会使旧共同祖先失效。共享缓存保持完整提交祖先，省去的是重复历史对象与未使用的历史文件内容，不能保证任意远端都支持部分获取。修复提交仍走现有宿主非 force push 门禁。

## 跨提交续审（#51）

会话只在 head 相同的显式跟进中复用；新 head 从新会话开始，通过有界 `previous_review` 接收上一次成功发布的发现及两次提交间的路径变化，不继承完整聊天历史。历史背景最多 16 KiB、变化路径最多 200 条，截断与旧提交不可用均显式标记。较大 PR 的初次审查仍逐批新建会话；同 head 跟进使用最后一次会话，并可按需读取整个检出。

`reviews.json` 分别保存最近会话的 head、已开始自动尝试的 SHA，以及最后成功发布审查的 `reviewed_sha`。模型失败、评论发布失败不会推进已审基线。历史发现通过稳定 ID 连接，未提及的发现保留为“待确认”；标为“仍存在”或“已解决”必须再次读取实际证据。宿主检查 ID、路径与读取范围，无法确定模型的语义判断是否正确。后续评论显示新增、仍存在、已解决及待确认数量；旧记录没有发现摘要时从首次成功审查建立，不推测历史结论。
