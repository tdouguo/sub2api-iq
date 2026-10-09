# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与
[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [0.0.1] - 2026-10-09

首个版本。基于 Sub2API 插件框架（`openai.oauth.outbound_transport.v1` 能力）实现，
在转发 OpenAI OAuth 出站请求的同时异步评测对话质量。

### Added

- 出站转发：基于 `sdk/transport.Pool` 按代理地址复用连接池，保留宿主为账号解析的代理配置
- 防循环检测：请求 host 与评测接口 host 相同时跳过评测，避免自我调用无限递归
- 分级评测策略：总开关 → 账号规则 → 默认策略，支持账号级采样率
- 流式安全采集：旁路复制字节，不预先缓冲响应，SSE 流式输出语义不受影响
- 异步评测队列：有界队列 + 固定工作协程，队列满按配置丢弃而不拖慢转发
- 评分模型调用：兼容 OpenAI `/chat/completions`，指数退避重试，`temperature=0` 保证可复现
- 四维评分：`Overall = ReasoningDepth×0.3 + JuiceEfficiency×100×0.2 + LogicCoherence×0.3 + TaskCompletion×0.2`，
  其中 `JuiceEfficiency = reasoning_tokens / total_tokens` 由响应 usage 直接计算
- 对话还原：同时支持普通 JSON 响应与 SSE 流式响应，兼容 `content` 为字符串或分块数组
- 结果存储：写入宿主 KV（命名空间 `iq`），每条记录独立成键，按账号前缀列举读取
- 配置页：内联自包含 UI（无外部依赖，适配宿主 `default-src 'none'` CSP），
  通过官方 Bridge v1 读写配置、保存并测试、轮询只读状态
- 一致性测试：接入框架 `plugintest.RunConformance`，覆盖身份、配置生命周期、转发帧协议与流式转发

### 已知限制

- `request_sent` 语义：遵循 `sdk/transport` 规范，仅能证明未触达上游时判为未发出
- `loop_detection.check_model` 保留但恒不生效 —— 比对 model 需预读请求体，
  会导致「拨号失败」被误判为已发出，使宿主放弃换号重试
- `requires.tested_sub2api_versions` 为空，插件尚未在真实宿主上验证

[Unreleased]: https://github.com/tdouguo/sub2api-iq/compare/v0.0.1...HEAD
[0.0.1]: https://github.com/tdouguo/sub2api-iq/releases/tag/v0.0.1
