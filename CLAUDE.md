# CLAUDE.md — Sub2API IQ 插件项目交接文档

> 本文件为 Claude Code 会话自动加载的项目上下文。
> 当前状态：**已在 Code 模式下完成重写，可编译、测试通过、打包链路验证成功**。

---

## 一、项目定位

| 项 | 值 |
|---|---|
| 本地路径 | `F:\Workspace\Github\sub2api-iq` |
| 目标仓库 | `https://github.com/tdouguo/sub2api-iq` |
| 上游宿主 | [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) |
| 插件框架 | [feeeei/sub2api-plugin-framework](https://github.com/feeeei/sub2api-plugin-framework) |
| 首个 tag | **`0.0.1`**（已统一 manifest 与 CHANGELOG） |
| Go 工具链 | **1.26.9**（`go.mod` 声明，构建时自动切换工具链） |

### 插件要做什么

宿主把 OpenAI OAuth 账号的上游 HTTP 请求交给插件发出（`RoundTrip`）。本插件在转发的同时：

1. 捕获请求/响应（旁路采集，不破坏流式）
2. **防循环检测** —— 请求目标与「IQ 评测 API」同 host 时跳过评测
3. 按 **账号** 配置判断是否需要评测（含采样率）
4. 异步调用 `swift-1.5-iq3_xxs` 类模型做 IQ 评分
5. 存评分结果到宿主 KV

### 评分维度

```
JuiceEfficiency = reasoning_tokens / total_tokens
Overall = ReasoningDepth×0.3 + JuiceEfficiency×100×0.2 + LogicCoherence×0.3 + TaskCompletion×0.2
```

---

## 二、关键结论：框架真实契约（已通过读源码验证）

**框架仓库无任何 tag**，只有 `main` 分支。`go.mod` 使用伪版本：

```
github.com/feeeei/sub2api-plugin-framework v0.0.0-20260924064648-d84fcd4fe708
```

此前代码假设的 API 与实际契约的差异（全部已修正）：

| 曾经的假设 | 真实契约 |
|---|---|
| `sdk.Info{ID,Name,Version,Author,Description}` | 只有 `ID`/`Version`/`Capabilities` |
| `sdk.RequestMeta.UserID` | **不存在**；只有 `AccountID int64`、`Platform`、`AccountType` |
| `sdk.NewServer(p).Serve()` / `.SetDebug()` | **只有** `sdk.Serve(p, opts...)` |
| `sdk.HostService`（`GetConfig`/`SetConfig`/`KVGet`/`KVSet`） | `sdk/hostsvc.Client`：`KV().Get/Set/Delete/List`、`Accounts().List/ResolveIdentity` |
| `plugin.Init()` / `plugin.Shutdown()` | **不存在**；用 `HostServicesAware.SetHostServices` |
| `ValidateConfig(raw json.RawMessage) error` | `ValidateConfig(ctx, raw []byte) ([]byte, error)` |
| `manifest_version` / `icon` / `permissions` / `settings` / `capabilities{...}` | 宿主 `DisallowUnknownFields`，**全部会致安装失败**。真实字段见 `manifest.source.json` |

---

## 三、当前文件清单（30 个文件）

```
sub2api-iq/
├── .github/
│   ├── release-drafter.yml
│   └── workflows/
│       ├── build-plugin.yml          # go1.26.9 + gofmt/vet/test + 交叉编译 + s2plugin pack/verify + hostemu 冒烟
│       └── code-quality.yml          # golangci-lint + govulncheck
├── .gitignore                        # 已修 P1-11：不再误伤文档
├── CHANGELOG.md                      # 0.0.1
├── CLAUDE.md                         # 本文件
├── LICENSE                           # MIT（已补 P2-2）
├── README.md                         # 已重写为插件模式
├── docs/
│   ├── README.md                     # 索引，链接已修（全小写）
│   ├── architecture.md               # 插件模式架构
│   ├── configuration.md              # 全字段说明
│   └── integration.md                # 安装/签名/灰度/排障
└── plugin/
    ├── Makefile                      # 走官方 s2plugin pack
    ├── go.mod / go.sum               # module = github.com/tdouguo/sub2api-iq/plugin
    ├── manifest.go                   # go:embed manifest.source.json
    ├── manifest.source.json          # 严格符合框架 schema
    ├── cmd/plugin/main.go            # 仅 2 行
    ├── internal/plugin/
    │   ├── plugin.go                 # 身份/配置生命周期/RoundTrip/Health/TestConfig
    │   ├── config.go                 # Codec[Config] 严格校验
    │   ├── capture.go                # 旁路采集器（流式安全）
    │   ├── conversation.go           # JSON + SSE 对话还原
    │   ├── scorer.go                 # 评分调用 + 指数退避
    │   ├── queue.go                  # 有界队列 + worker pool
    │   ├── storage.go                # KV 读写（无竞态）
    │   ├── plugin_test.go            # 含 RunConformance
    │   └── storage_test.go           # 并发写入不丢记录等
    └── ui/
        ├── index.html
        └── assets/{app.css,app.js,bridge-v1.js}   # bridge 为框架官方版
```

---

## 四、缺陷修复对照

### P0（全部已修）

| # | 问题 | 解决方式 |
|---|---|---|
| P0-1 | 缺 `bytes` import | 该文件已整体重写 |
| P0-2 | 无 `go.sum` | `go mod tidy` 生成 |
| P0-3 | SDK API 全凭猜测 | **已拉源码逐个核对**，按真实接口重写 |
| P0-4 | `storage` 为 nil 会 panic | 改为惰性取用 `p.current()`，host 可空降级 |
| P0-5 | 破坏流式响应 | `captureReader` 旁路采集，边转发边收集；`streaming_response` 一致性测试通过 |
| P0-6 | 引用不存在的 icon | manifest 已无 icon 字段（宿主 schema 也不接受） |
| P0-7 | docs 链接大小写失效 | 文档统一为全小写并在索引中引用 |

### P1

| # | 状态 | 说明 |
|---|---|---|
| P1-1 | ✅ | 实现 `ConfigHandler` + `Codec[Config]`，规范化幂等 |
| P1-2 | ✅ | Makefile 与 CI 改用 `s2plugin pack/verify` |
| P1-3 | ✅ | 用 `transport.Pool`，按账号代理复用连接 |
| P1-4 | ✅ | `transport.Wrap` + `RequestSent(err, bodyTouched)`；`unreachable_marks_request_not_sent` 通过 |
| P1-5 | ✅ | 实现有界队列 + worker pool，配置项不再是死配置 |
| P1-6 | ✅ | 指数退避重试（200ms/400ms…，仅 429/5xx/网络错误重试） |
| P1-7 | ✅ | `check_model` 保留字段但**恒不生效**（原因见下） |
| P1-8 | ✅ | 每条记录独立成键 + 前缀列举，消除读-改-写竞态（有并发测试覆盖） |
| P1-9 | ✅ | UI 完全内联，无 CDN 依赖，适配 `default-src 'none'` |
| P1-10 | ✅ | 直接使用框架官方 `ui/bridge-v1.js` |
| P1-11 | ✅ | `.gitignore` 重写，不再用 `*_GUIDE.md` 等宽泛规则 |
| P1-12 | ✅ | module 改为 `github.com/tdouguo/sub2api-iq/plugin` |
| P1-13 | ✅ | 文档重写为插件模式，删除描述已废弃独立架构的文件 |
| P1-14 | ✅ | 根 README 重写 |
| P1-15 | ✅ | 删除 PostgreSQL schema/seed 脚本（插件走宿主 KV） |

### P2

| # | 状态 |
|---|---|
| P2-1 | ✅ 版本统一 0.0.1 |
| P2-2 | ✅ 已补 LICENSE |
| P2-3 | ✅ 接入 `plugintest.RunConformance` + 单元测试 |
| P2-4 | ✅ `tested_sub2api_versions` 留空（未真实验证过就不声明） |
| P2-5 | ✅ `permissions.allowed_hosts` 字段已移除（宿主 schema 无此字段） |
| P2-6 | ✅ CI 移除 codecov/gosec 噪音，改为 gofmt+vet+test 与 govulncheck |
| P2-7 | ✅ `RequestMeta` 字段类型已按真实定义对齐 |
| P2-8 | ✅ 语义明确：只有账号维度，不存在「账号当分组用」 |

---

## 五、两个必须记住的设计约束

1. **防循环只能比对 host，不能比对 model。**
   比对 model 需要预读请求体，这会让 SDK 判定请求已开始写向上游，
   从而把「拨号失败」误报成 `request_sent=true`，使宿主放弃换号重试。
   `loop_detection.check_model` 因此保留字段但恒为 `false`。

2. **宿主 KV 的 key 只允许 `[A-Za-z0-9._-]`。**
   分隔符必须用 `.`，用 `/` 或 `:` 会被判 `InvalidArgument`。
   记录键格式：`<account_id>.<13位毫秒时间戳>-<request_id 片段>`，
   零填充使字典序等于时间序，裁剪最旧记录无需读取内容。

---

## 六、常用命令

```bash
cd plugin
go build ./...          # 编译
go vet ./...            # 静态检查
go test ./... -count=1  # 测试（含框架一致性套件）
```

打包与自检（本机无 make 时直接调用）：

```bash
go build -o dist/bin/windows-amd64/plugin.exe ./cmd/plugin
go tool s2plugin pack -manifest manifest.source.json -ui ui -output dist/sub2api-iq.s2plugin -runtime windows-amd64=dist/bin/windows-amd64/plugin.exe
go tool s2plugin verify -allow-unsigned -host-version 0.2.8 -platform windows-amd64 dist/sub2api-iq.s2plugin
go tool s2plugin run -package dist/sub2api-iq.s2plugin info
```

---

## 七、已验证 / 未验证

**已验证**（本机实测通过）：

- `go build`、`go vet`、`gofmt` 全部干净
- `go test ./...` 全绿，含 `plugintest.RunConformance` 全部子测试
  （其中 `streaming_response` 与 `unreachable_marks_request_not_sent` 是关键回归防线）
- `s2plugin pack` 生成包，`s2plugin verify` 通过
- `s2plugin run ... info` 在 hostemu 中成功握手：身份一致、健康、宿主服务就绪

**未验证**：

- `go test -race`：本机无 gcc（cgo 不可用），仅在 CI（ubuntu）上能跑
- 真实 Sub2API 宿主上的端到端运行；因此 `tested_sub2api_versions` 留空
- 真实 IQ 评分 API 的联调（配置页「测试」按钮可验证）

---

## 八、待用户确认

1. **评分 API 地址与模型名**：当前默认 `swift-1.5-iq3_xxs`，`base_url` 留空待填。
2. **是否设置 `tested_sub2api_versions`**：在真实宿主验证通过后再填 `["0.2.8"]`。
3. **发布签名密钥**：`make keygen` 生成后需把公钥加入宿主 `plugins.trusted_publishers`。
4. **远端仓库当前状态**：本机 `git` 已初始化并有一次基线快照提交（未推远端），
   推送前需确认远端是否为空。
