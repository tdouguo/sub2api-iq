# Sub2API IQ Evaluator

Sub2API 出站传输插件：在转发 OpenAI OAuth 账号的上游请求的同时，异步调用 IQ 评分模型
对每次对话的推理质量打分并留存记录。

- 插件 ID：`com.sub2api.iq`
- 能力：`openai.oauth.outbound_transport.v1`（`platform=openai`，`account_type=oauth`）
- 宿主要求：Sub2API `>=0.2.8 <0.3.0`，`plugin_protocol=1`、`transport_api=1`、`ui_bridge=1`
- 当前版本：`0.0.1`

## 它做什么

宿主把命中灰度策略的 OpenAI OAuth 上游请求交给插件的 `RoundTrip`。插件在把请求发给真实上游的同时：

1. **防循环检测** —— 若请求目标与评测 API 同站点，跳过评测。缺少该检查会导致插件评测自己的评测请求，无限递归。
2. **按账号策略判断** —— 总开关 → 账号规则 → 默认策略，并支持采样率。
3. **边转发边采集** —— 用旁路采集器复制字节，绝不预先读完整条响应流，因此 SSE 流式输出语义不受影响。
4. **异步评分** —— 响应流读完后把任务投入有界队列，调用兼容 OpenAI `/chat/completions` 的评分模型。
5. **落库** —— 结果写入宿主 KV（命名空间 `iq`），按账号前缀列举读取。

评分公式：

```
JuiceEfficiency = reasoning_tokens / total_tokens          # CoT 长度占比，客观量，不由评委模型给出
Overall = ReasoningDepth × 0.3
        + JuiceEfficiency × 100 × 0.2
        + LogicCoherence × 0.3
        + TaskCompletion × 0.2
```

其中三个主观维度（0–100）由评分模型给出，`JuiceEfficiency` 从响应的 usage 字段直接计算。

## 仓库结构

```
sub2api-iq/
├── plugin/                        # 插件工程（Go module）
│   ├── manifest.source.json       # 清单源文件；身份由它嵌入二进制，与打包器共用
│   ├── manifest.go                # go:embed 清单，供 main 推导 GetInfo
│   ├── Makefile                   # build / pack / verify / keygen，走官方 s2plugin
│   ├── cmd/plugin/main.go         # 进程入口，仅两行
│   ├── internal/plugin/           # 插件实现
│   │   ├── plugin.go              # 身份、配置生命周期、RoundTrip、Health、TestConfig
│   │   ├── config.go              # 严格配置解析与校验（Codec[Config]）
│   │   ├── capture.go             # 旁路采集器：边转发边收集，保证流式
│   │   ├── conversation.go        # 对话还原：普通 JSON 与 SSE 两种响应
│   │   ├── scorer.go              # 评分模型调用、指数退避重试、分数合成
│   │   ├── queue.go               # 有界评测队列与工作协程
│   │   └── storage.go             # 评测记录读写（宿主 KV）
│   └── ui/                        # 配置页（内联资源，无外部依赖）
└── docs/                          # 架构、配置、集成文档
```

## 构建与打包

需要 Go 1.25+（`go.mod` 声明，构建时自动切换工具链）。打包走框架官方 CLI：

```bash
cd plugin
make test          # go vet + gofmt + 单元测试（含框架一致性套件）
make pack          # 交叉编译并用 s2plugin pack 生成 dist/sub2api-iq.s2plugin
make verify        # 以宿主安装规则校验包
make keygen        # 生成 ed25519 发布者密钥
```

带签名发布：

```bash
make pack SIGNING_KEY=build/keys/publisher.private KEY_ID=publisher-v1
```

宿主侧需在 `config.yaml` 中把公钥加入 `plugins.trusted_publishers`（`make keygen` 会打印可粘贴的配置片段）。
未签名的包只能在 `plugins.allow_unsigned=true` 的宿主上安装。

本地自检（宿主由 hostemu 模拟）：

```bash
go tool s2plugin run -package dist/sub2api-iq.s2plugin info
go tool s2plugin run -package dist/sub2api-iq.s2plugin health
go tool s2plugin run -package dist/sub2api-iq.s2plugin forward -url https://api.openai.com/v1/models
```

## 默认配置

插件安装后默认**不评测任何请求**（`enabled=false`），避免立即产生流量与费用。
启用前需至少填写 `iq_api.base_url` 与 `iq_api.model`。完整字段见
[配置说明](docs/CONFIGURATION.md)。

## 已知限制

- **仅按账号配置**。框架下发的 `RequestMeta` 只含 `AccountID`/`Platform`/`AccountType`，
  没有用户维度，因此无法实现用户级策略。
- **防循环只比对 host**。比对请求体中的 `model` 需要预读请求体，而那会让 SDK 把
  「拨号失败」误判为 `request_sent=true`，导致宿主放弃换号重试。因此 `check_model`
  保留在配置中但恒为 `false`。
- **`tested_sub2api_versions` 为空**。插件尚未在真实宿主上验证过，因此不声明已测版本；
  宿主会显示为 `untested`，需管理员确认后启用。
- **评测记录无跨实例删除锁**。裁剪最旧记录依赖按时间戳排序的键名，多实例并发裁剪
  可能少量多删，不会损坏数据。

## 许可证

[MIT](LICENSE)
