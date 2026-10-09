# 架构

## 1. 插件在宿主链路中的位置

宿主把命中灰度策略的 OpenAI OAuth 上游请求交给插件进程，`RoundTrip` 负责把请求发往真实上游
并原样回传响应。宿主自己负责账号选择、Token 生命周期、响应解析、SSE 转交与计费；
插件只做两件事：**转发**，以及在不影响转发的前提下**评测**。

```
宿主 ──Forward 帧流──▶ 插件进程
                        │
                        ├─ 1. 防循环检测（同站点则跳过评测）
                        ├─ 2. 账号策略判断（总开关 → 账号规则 → 默认）
                        ├─ 3. transport.Pool 转发（沿用宿主下发的代理）
                        └─ 4. 旁路采集 ──▶ 有界队列 ──▶ 评分模型 ──▶ 宿主 KV
```

## 2. 请求生命周期

`RoundTrip(req, meta)` 的执行顺序，以及每一步为何这样安排：

1. **配置未就绪即拒绝**，返回 `transport.NotSent(NOT_CONFIGURED)`。
   请求确定未发出，宿主可以安全地换号重试。
2. **防循环检测**（`isLoopRequest`）。评测 API 若被配置成同一个 Sub2API 实例，
   插件发出的评测请求会再次流经本插件，形成无限递归。只比对 host。
3. **账号策略判断**（`shouldEvaluate`）。不评测时**完全不包装请求体**，
   零额外开销，也不改变任何请求语义。
4. **转发**（`transport.Pool.RoundTrip`）。用框架的连接池而非自建 `http.Client`，
   才能按代理地址复用连接并保留宿主为账号解析出的代理配置。
   出错时用 `transport.Wrap` 包装，`request_sent` 由是否读过请求体与错误类型共同决定。
5. **包装响应体后立即返回**。`RoundTrip` 只负责挂上采集器，**不读取响应**。

## 3. 流式响应为什么必须边转发边采集

宿主把插件返回的 `resp.Body` 当流式管道消费：SSE 场景下首字节必须尽快到达客户端。
如果插件先 `io.ReadAll` 再交还，整条流会被缓冲——流式语义失效，Codex / Claude Code
这类客户端直接不可用。

因此 `captureReader` 采用旁路复制：

```
宿主 Read(p) ──▶ captureReader.Read ──▶ 上游 Body.Read
                        │
                        └─ 复制已读字节到内存缓冲（超过上限即停止收集，仍继续转发）
```

评测任务在**响应流结束时**（读到 `io.EOF`，或宿主关闭响应体）才投递，
保证拿到的是完整内容。若宿主提前关闭（客户端断开、上游读失败），
`Complete()` 为 `false`，该次评测被跳过 —— 避免把半截内容当完整对话打分。

框架一致性套件中的 `streaming_response` 子测试专门防止这一行为退化。

## 4. 并发模型

| 组件 | 并发策略 |
| --- | --- |
| 配置 | `sync.RWMutex` 保护；`ApplyConfig` 原子替换配置、连接池与队列 |
| 计数器 | `atomic.Int64`（转发数、评测数、失败数、丢弃数、防循环跳过数） |
| 采样随机数 | 独立 `rand.Rand` + 互斥锁（`math/rand` 全局源不够用） |
| 评测队列 | 有界 channel + 固定数量工作协程 |

**队列为什么必须有界**：转发路径不能因为评测慢而堆积协程。若直接 `go f(...)`，
并发一高就是无界协程与无界内存，同时把评分 API 打满。队列满时默认丢弃新任务
（`drop_when_full=true`）；关闭丢弃时最多等待 200ms，仍然满则放弃——
宁可少评一条，也不能拖住宿主转发。

配置切换时旧队列会被关闭（`close()`），在途任务被放弃，因为其配置已经过期。

## 5. 存储设计

评测记录写入宿主 KV，命名空间固定为 `iq`。

键名：`<account_id>.<13位毫秒时间戳>-<request_id 片段>`

这里有两个刻意的选择：

1. **每条记录独立成键，不维护「账号 → ID 列表」**。
   宿主 KV 没有 CAS，`KVGet → append → KVSet` 在并发评测时会互相覆盖、静默丢记录。
   改为按键前缀列举后不存在读-改-写，天然无竞态。
2. **分隔符用 `.` 而不是 `/`**。宿主对 KV 的 namespace/key 只接受 `[A-Za-z0-9._-]`，
   用 `/` 会被判为 `InvalidArgument`。用 `.` 还顺带避免了账号 `4` 与 `42` 的前缀互相误匹配。

时间戳零填充到 13 位，因此**字典序即时间序**，裁剪最旧记录无需读取内容。

## 6. 与框架的接口对应关系

| 框架接口 | 本插件实现 | 说明 |
| --- | --- | --- |
| `sdk.Plugin` | `Info` / `RoundTrip` | 必需 |
| `sdk.ConfigHandler` | `ValidateConfig` / `ApplyConfig` | 严格解析，未知字段拒绝 |
| `sdk.ConfigTester` | `TestConfig` | 配置页「测试」按钮 |
| `sdk.HealthReporter` | `Health` | 附带只读 `status_json` |
| `sdk.HostServicesAware` | `SetHostServices` | 宿主未提供时优雅降级 |

`ValidateConfig` 与 `ApplyConfig` 共用同一个 `sdk/config.Codec[Config]`，
保证两者行为一致；`ApplyConfig` 幂等，以支持宿主写库失败时的回滚重放。
