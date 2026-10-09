# 集成与部署

## 1. 环境要求

| 项 | 要求 |
| --- | --- |
| Go | 1.26.9 或更高（`go.mod` 声明 `go 1.26.9`，构建时自动切换工具链） |
| 宿主 | Sub2API `>=0.2.8 <0.3.0` |
| 宿主配置 | 需要开启插件功能；未签名包要求 `plugins.allow_unsigned=true` |

## 2. 构建

```bash
cd plugin
make test
make pack
```

`make pack` 会交叉编译 `TARGETS` 中的全部平台，并用框架打包器生成
`dist/sub2api-iq.s2plugin`。打包器会：

- 校验 `manifest.source.json` 的字段与取值；
- 为每个运行时与 UI 文件计算 SHA-256 写入 `files`；
- 生成规范的 `manifest.json`（未签名包不含 `signature.json`）。

默认目标平台为 `linux-amd64 linux-arm64 darwin-arm64 windows-amd64`，另外总是包含
本机平台（宿主只接受声明了自身 `<goos>-<goarch>` 运行时的包）。

## 3. 签名

生产环境的包必须签名，否则只能在 `plugins.allow_unsigned=true` 的宿主上安装。

```bash
make keygen                      # 生成 build/keys/publisher.private 与 .public
make pack SIGNING_KEY=build/keys/publisher.private KEY_ID=publisher-v1
```

`make keygen` 会打印可直接粘贴到宿主 `config.yaml` 的配置片段：

```yaml
plugins:
  trusted_publishers:
    publisher-v1: "<Base64 公钥>"
```

`KEY_ID` 必须与 `trusted_publishers` 中的键一致。**私钥绝不入库**，
`.gitignore` 已排除 `*.private` 与 `plugin/build/keys/`。

## 4. 安装与启用

1. 在宿主管理页 `插件` 中上传 `.s2plugin`。宿主会以 `DisallowUnknownFields` 解析清单、
   校验签名、逐个核对文件哈希后才安装。
2. 插件状态会显示兼容性：
   - `incompatible` —— `requires.sub2api` 范围外，不可启用；
   - `untested` —— 在范围内但未列入 `tested_sub2api_versions`，需管理员确认；
   - 正常 —— 可直接启用。
3. 全局同一时刻只允许一个 OpenAI OAuth 出站传输插件处于启用状态。
4. 启用后先进入配置页填写 `iq_api.base_url` 与 `iq_api.model`，再打开总开关。

启动时宿主会校验插件身份：`GetInfo` 返回的 `plugin_id` 与 `plugin_version`
必须与已安装清单完全一致，否则立即终止进程。本插件的身份由 `go:embed` 的
`manifest.source.json` 推导，与打包器读的是同一份数据，因此不会漂移。

## 5. 灰度与路由

只有 `platform=openai` 且 `account_type=oauth` 的账号可能命中插件。
命中判定为 `hash(account_id) % 100 < rollout_percent`，按账号稳定分桶。

命中且插件不可用时请求**直接报错，不回落内置直连**（失败关闭）。
因此上线前请确认插件已启用且健康，避免影响命中账号的可用性。

## 6. 本地自检

用 hostemu 模拟宿主拉起已打包的插件，无需真实宿主：

```bash
cd plugin
go tool s2plugin run -package dist/sub2api-iq.s2plugin info
go tool s2plugin run -package dist/sub2api-iq.s2plugin health
go tool s2plugin run -package dist/sub2api-iq.s2plugin validate -config '{"enabled":true}'
go tool s2plugin run -package dist/sub2api-iq.s2plugin forward -url https://api.openai.com/v1/models
```

单元测试内含框架标准一致性套件（`plugintest.RunConformance`），覆盖身份、
配置前健康、配置生命周期与转发帧协议，其中 `streaming_response` 子测试
专门防止流式转发被缓冲的回归。

## 7. 排障

先看配置页顶部的**运行状态**，它来自 `Health` 的 `status_json`：

| 字段 | 含义与排查方向 |
| --- | --- |
| `enabled` | 总开关是否为 `true` |
| `requests` | 转发请求数。长期为 0 说明没有账号命中灰度 |
| `evaluated` | 完成评测数。远小于 `requests` 时看下面几项 |
| `loop_skips` | 防循环跳过数。持续增长说明评测接口与被评测流量同站点 |
| `dropped` | 丢弃任务数。持续增长说明评测慢于转发，需调大 `worker_count` |
| `failures` | 失败数。结合 `iq_api_base_url` 检查评测接口可达性 |
| `queued` | 当前排队数。长期接近 `queue.max_size` 说明队列容量不足 |
| `host_services` | 宿主服务是否握手。为 `false` 时评测结果无法落库 |

常见问题：

- **配置页白屏** —— 宿主 CSP 为 `default-src 'none'`，任何 CDN 引用都会被拦截。
  本插件的 UI 资源全部内联在包内，若仍白屏请检查 `ui/` 下文件是否齐全。
- **保存失败** —— 配置页会原样展示插件返回的校验消息，对照
  [配置说明](configuration.md) 的字段范围逐项检查。
- **评测结果查不到** —— 确认 `host_services` 为 `true`，且宿主已配置 KV 存储
  （未配置时宿主服务返回 `Unavailable`，插件会静默跳过落库）。
- **响应变慢或流式失效** —— 这属于严重回归。`captureReader` 设计上不会缓冲整个响应，
  请确认没有把 `resp.Body` 换成先读后回的实现，并运行 `go test ./internal/...`
  看 `streaming_response` 是否仍然通过。
