# AGENTS.md

本文件面向在本仓库工作的 AI Agent（Claude Code、Codex 等），描述项目目标、Sub2API 插件机制、
本仓库架构与必须遵守的设计约束。人类的交接文档见 `CLAUDE.md`，两者分工不同：本文件说明「做什么、怎么做、为什么」，
`CLAUDE.md` 记录「已修过哪些缺陷」。

---

## 零、行为准则：交付干净终态

严禁「纠偏后残留历史痕迹与自我辩解」的行为模式：

> 用户让做一盘「番茄炒蛋」，Agent 擅自加了「东坡肉」；被指出后虽然去掉了，但提交/PR 时写着
> 「番茄炒蛋（无东坡肉）」，并在注释中大篇幅解释为什么本道菜不需要加东坡肉。

**交付要求**：任何代码、注释、文档、提交信息及回复，都必须直接呈现对齐后的**干净终态设计**
（Clean final-state design），严禁包含任何前序错误、自我辩解或「在此删掉某逻辑」的纠错痕迹。

---

## 一、项目目标

让 Sub2API 在转发 OpenAI OAuth 账号上游请求时，**旁路**对每次对话做一次推理质量（IQ）评分并留存记录，
用于评估不同模型/账号的「推理含金量」。核心约束是三条不可逾越的底线：

1. **不破坏转发语义** —— 尤其是 SSE 流式输出与 `request_sent` 语义。
2. **不影响宿主决策** —— 评测永远异步，失败只丢评分，绝不阻断或重试上游请求。
3. **可解释、可复现** —— 评分维度公式固定，评委模型温度固定为 0。

评分公式：

```
JuiceEfficiency = reasoning_tokens / total_tokens      # 客观量，取自响应 usage，不由评委给出
Overall = ReasoningDepth × 0.3
        + JuiceEfficiency × 100 × 0.2
        + LogicCoherence × 0.3
        + TaskCompletion × 0.2
```

---

## 二、上游权威资料

| 用途 | 地址 |
|---|---|
| 插件开发总纲（**机制以它为准**） | https://github.com/Wei-Shaw/sub2api/blob/main/docs/PLUGIN_DEVELOPMENT.md |
| 宿主仓库 | https://github.com/Wei-Shaw/sub2api |
| 公开协议源码（**比文档更权威**） | `backend/pkg/pluginapi/`（`plugin.proto`、`manifest.schema.json`、`docs/`） |
| 本项目使用的 Go 插件框架 | https://github.com/feeeei/sub2api-plugin-framework |
| 本仓库 | https://github.com/tdouguo/sub2api-iq |

> 冲突时的优先级：**宿主 `backend/pkg/pluginapi/` 源码 > 官方文档 > 框架仓库源码 > 本仓库文档**。
> 框架仓库无任何 tag，`go.mod` 用伪版本锁定，升级时必须重新核对 API 而非假设。

---

## 三、Sub2API 插件机制

### 3.1 进程与边界

插件是**独立进程 + 静态 UI** 构成的单个 `.s2plugin` 包（内含运行时可执行文件、UI 资源与清单）。
宿主**不链接**插件代码，双方通过一套版本化的 gRPC 协议通信。因此：

- 插件崩溃、卡死、配置错误都由宿主按协议处理，不会污染宿主进程。
- 跨边界的一切都必须是显式契约，不能依赖进程内共享状态。

### 3.2 能力（capability）

目前宿主**只实现了一个**能力：

```
openai.oauth.outbound_transport.v1    platform=openai  account_type=oauth
```

含义是：宿主把命中该能力的 OpenAI OAuth 账号的上游 HTTP 请求，交给插件发出。
**声明宿主未实现的能力不会自动产生路由** —— 扩展 Provider / 账号类型 / 消息字段必须先版本化公开协议，
再由宿主增加能力匹配与生命周期处理。

### 3.3 清单（manifest）

- 只维护 `plugin/manifest.source.json`，**绝不手工编辑构建目录里的 `manifest.json`**。
- 必填字段：`schema_version`、`id`、`name`、`version`、`requires`、`capabilities`、`runtimes`、`ui`、`files`。
- 宿主用 `DisallowUnknownFields` 解析，**多写一个字段就装不上**。
- 打包器自动填充各平台运行时、UI 与文件 SHA-256；`requires` 里 `sub2api` 是**硬兼容范围**，
  `tested_sub2api_versions` 只能填**真正验证过**的版本。
- 签名覆盖最终 `manifest.json` 的精确字节，**签名后不得再格式化该文件**。

### 3.4 传输契约（TransportPlugin）

| 方法 | 契约要点 |
|---|---|
| `GetInfo` | 返回的 ID / 版本 / 协议版本 / 能力必须与已装清单完全一致，否则宿主立即终止进程 |
| `Health` | 快速返回，**不做长时间网络探测** |
| `ValidateConfig` | 严格解析，拒绝未知字段与非法范围，返回补齐默认值的**完整规范化配置** |
| `ApplyConfig` | 成功后**原子**切换配置；失败保留旧配置与旧连接；宿主回滚时会重复调用，**必须幂等** |
| `TestConfig` | 针对**已保存**配置做快速诊断 |
| `Forward` | 按流帧接收：`start` → 0..n `body_chunk` → `body_end`；响应：`start` → 0..n `body_chunk` → `end`；出错发 `error` 帧 |

### 3.5 两条最容易被写错的语义

**A. `request_sent` 必须准确。**
`ForwardResponseError.request_sent` 只有在**能确认尚未调用上游 HTTP Transport** 时才可为 `false`；
一旦调用过、或无法确认上游是否收到，就必须为 `true`。宿主据此决定是否**换号重试** ——
误报 `false` 会让宿主重复执行同一次请求（可能重复计费、重复副作用）。

**B. 资源与上下文。**
复用 HTTP Transport 与连接池；配置切换时关闭旧空闲连接；沿用 gRPC stream 的 context 取消
DNS/连接/上传/响应读取；**始终关闭上游响应体**。日志与错误消息不得包含 Token、代理凭据、
完整请求体或敏感响应头。

### 3.6 配置与 UI

- 配置**由插件定义字段与默认值**，由 Sub2API 加密保存。JSON 字段统一 `snake_case`。
- 推荐流程：定义 `Config` 结构 → 严格解析 → 空对象规范化为完整默认配置 →
  `ValidateConfig` 与 `ApplyConfig` **复用同一套校验**。
- 敏感配置不进 URL、UI 通知、诊断结果或日志；插件**不得**从 UI 读取/刷新/持久化 OAuth Token。
- UI 是包内静态页面，宿主在受限 iframe 中加载 `ui/index.html`，走 UI Bridge 消息
  （`config.load` / `config.save` / `config.test` / `ui.resize` / `ui.notify`），
  每条消息带 `request_id` 并校验来源与 Bridge Token。UI **不得依赖 CDN、远程脚本、Cookie 或本地存储**。

### 3.7 打包、签名与安装

- 打包/校验统一走框架官方 CLI：`go tool s2plugin pack | verify | run | keygen`。
- 签名用 **Ed25519**：私钥仅存受控开发机或 CI Secret，**绝不入库/入包**。
- 宿主 `plugins.trusted_publishers` 把 `key_id` 映射到 Base64 公钥，且**不能覆盖内置官方公钥**；
  `signature.json` 的 `key_id` 必须与配置键**完全一致**。
- 宿主默认拒绝未签名包（`allow_unsigned=false`）；开发期临时置 `true` 后必须立即恢复。

---

## 四、本仓库架构

### 4.1 运行时数据流

```
宿主（账号选择 / Token 生命周期 / 计费）
   │  Forward 流帧（命中灰度策略的 OpenAI OAuth 上游请求）
   ▼
Plugin.RoundTrip ──① 防循环：目标与评测 API 同 host → 跳过评测
   │
   ├─② 按账号策略判定：总开关 → 账号规则 → 默认策略 → 采样率
   │
   ├─③ transport.Wrap 发出真实上游请求，旁路采集器
   │     边转发边复制字节（不预读，SSE 语义不变），并在结束时
   │     记录 RequestSent(err, bodyTouched) 以给出准确的 request_sent
   │
   ├─④ 响应流读完后还原对话（普通 JSON / SSE 两条路径）
   │     → 有界队列（worker pool）→ 异步评分，不阻塞转发
   │
   └─⑤ scorer 调评委模型（兼容 OpenAI /chat/completions，temperature=0）
          → 合成四维评分 → 写入宿主 KV（命名空间 iq）
```

失败面：队列满按 `drop_when_full` 丢弃；评分失败按 429/5xx/网络错误做指数退避重试；
KV 不可用时静默降级（`host` 可为 nil）。**任何一步失败都不影响已转发的响应。**

### 4.2 目录结构

```
sub2api-iq/
├── AGENTS.md                      # 本文件：Agent 工作说明
├── CLAUDE.md                      # 缺陷修复交接记录
├── README.md / CHANGELOG.md / LICENSE
├── docs/                          # architecture / configuration / integration
└── plugin/                        # Go module: github.com/tdouguo/sub2api-iq/plugin
    ├── manifest.source.json       # 清单唯一来源
    ├── manifest.go                # go:embed 清单 → 供 main 推导 GetInfo（防身份漂移）
    ├── Makefile                   # test / build / pack / pack-platform / verify / keygen / run-*
    ├── cmd/plugin/main.go         # 仅两行：解析嵌入清单 + sdk.Serve
    ├── internal/plugin/
    │   ├── plugin.go              # 身份、配置生命周期、RoundTrip、Health、TestConfig、防循环、采样
    │   ├── config.go              # Codec[Config] 严格解析 / 规范化 / 校验
    │   ├── capture.go             # 旁路采集器（流式安全）
    │   ├── conversation.go        # 对话还原：JSON + SSE，usage 提取
    │   ├── scorer.go              # 评委模型调用、指数退避、评分合成、诊断 probe
    │   ├── queue.go               # 有界队列 + worker pool
    │   ├── storage.go             # 宿主 KV 记录读写（无竞态）、裁剪、TTL
    │   └── *_test.go              # 含 plugintest.RunConformance 一致性套件
    └── ui/                        # 配置页：index.html + assets/（含框架官方 bridge-v1.js）
```

### 4.3 为什么要这样分层

- **入口极简**：`main.go` 只有两行。身份从嵌入清单推导，与打包器读同一份文件，避免「二进制自称的 ID/版本」
  与「包清单声明的 ID/版本」漂移 —— 漂移会让宿主在握手阶段直接杀掉进程。
- **逻辑全部在 `internal/plugin`**：可独立测试，不依赖进程启动。
- **采集与转发分离**：`capture.go` 只负责「边转发边攒字节」，`conversation.go` 只负责「字节 → 结构化对话」，
  `scorer.go` 只负责「对话 → 分数」。三者可分别单测，也保证流式路径上没有任何预读。
- **队列显式化**：`queue.go` 让 `worker_count` / `max_size` / `drop_when_full` 成为**真配置**而非装饰。

---

## 五、必须遵守的设计约束

### 5.1 防循环只能比对 host，不能比对 model

比对 model 需要**预读请求体**，而预读会让 SDK 判定请求已开始写向上游，从而把「拨号失败」
误报成 `request_sent=true`，使宿主放弃换号重试。因此 `loop_detection.check_model`
是保留字段，恒为 `false`。

### 5.2 宿主 KV 的 key 只允许 `[A-Za-z0-9._-]`

用 `/` 或 `:` 会被判 `InvalidArgument`。分隔符统一用 `.`，并且用 `.` 还能避免账号 `4` 与 `42`
的前缀互相误匹配。记录键格式：

```
<account_id>.<13 位零填充毫秒时间戳>-<request_id 片段>     键长上限 256
```

13 位零填充使**字典序 = 时间序**，裁剪最旧记录无需读取内容。
每条记录独立成键 + 前缀列举读取，**不做「账号 → ID 列表」的读-改-写** ——
宿主 KV 无 CAS，读-改-写在并发下会丢记录。

### 5.3 评测只按账号维度

框架下发的 `RequestMeta` 只有 `AccountID` / `Platform` / `AccountType`，**没有用户维度**。
不要发明用户级策略或「把账号当分组用」。

### 5.4 默认关闭

新装插件 `enabled=false`，避免一装上就产生流量与费用；启用前必须填 `iq_api.base_url` 与 `iq_api.model`。

---

## 六、常用命令

```bash
cd plugin
make test          # go vet + gofmt + go test（含框架一致性套件）
make build         # 交叉编译全部目标平台
make pack          # 通用包 dist/sub2api-iq-<version>-universal.s2plugin
make pack-platform PLATFORM=linux-amd64
make verify        # 以宿主安装规则校验包
make keygen        # 生成 ed25519 发布者密钥
```

本地自检（宿主由 hostemu 模拟）：

```bash
go tool s2plugin run -package dist/sub2api-iq-0.0.1-universal.s2plugin info
```

Go 版本：`go.mod` 声明 1.26.9，构建时自动切换工具链。

**未验证项**：`go test -race` 需 cgo/gcc（本机无，仅 CI 可跑）；真实宿主端到端运行；
真实 IQ 评分 API 联调。

---

## 七、改代码时请照做

1. **只改 `manifest.source.json`**，永不手工编辑构建产物里的 manifest；改字段前先对照
   `manifest.schema.json`，因为未知字段会导致安装失败。
2. **配置新增字段时**，同步更新：`config.go` 的默认值、`docs/configuration.md`、UI 表单，以及测试。
3. **不要碰转发主路径的同步行为**。任何新增的采集/解析都必须在异步侧或在 `captureReader` 的旁路副本上做。
4. **改动 `request_sent` 或 `transport.Wrap` 相关逻辑后**，必须确认一致性套件里的
   `unreachable_marks_request_not_sent` 与 `streaming_response` 仍然通过 —— 这两条是关键回归防线。
5. **`loop_detection.check_model` 保持 `false`**，不实现按 model 跳过评测的逻辑（约束见 5.1）。
6. 注释与文档用中文，与现有风格一致；不写「曾经如何、现在改掉了什么」的叙事。
