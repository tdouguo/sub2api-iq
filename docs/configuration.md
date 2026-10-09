# 配置

配置以严格 JSON 对象提交：**未知字段一律拒绝**，缺失字段保留默认值，
空输入或 `{}` 规范化为完整默认配置。字段名统一 snake_case。

配置由宿主加密保存，插件**不**自行持久化配置。

## 字段总览

```json
{
  "enabled": false,
  "default_enabled": false,
  "accounts": {
    "42": { "enabled": true, "sample_ratio": 1.0 }
  },
  "iq_api": {
    "base_url": "",
    "api_key": "",
    "model": "swift-1.5-iq3_xxs",
    "timeout_seconds": 30,
    "max_retries": 2
  },
  "loop_detection": {
    "enabled": true,
    "check_host": true,
    "check_model": false
  },
  "queue": {
    "worker_count": 4,
    "max_size": 256,
    "drop_when_full": true
  },
  "capture": {
    "max_request_body_bytes": 262144,
    "max_response_body_bytes": 262144,
    "max_records_per_account": 200,
    "record_ttl_hours": 0
  }
}
```

## 顶层开关

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | 总开关。关闭时插件只做纯转发，不产生任何评测流量与费用 |
| `default_enabled` | bool | `false` | 未被 `accounts` 命中的账号是否评测 |
| `accounts` | object | `{}` | 按账号覆盖策略，键为十进制账号 ID 字符串 |

> 默认关闭是刻意的：安装插件不应立即产生成本。`enabled=true` 时**必须**同时提供
> `iq_api.base_url` 与 `iq_api.model`，否则校验失败。

## 账号规则

键为宿主的 `account_id`（来自 `RequestMeta.AccountID`）。判定顺序为
**账号规则 → 默认策略**；命中规则时不再回落到 `default_enabled`。

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | 该账号是否评测 |
| `sample_ratio` | float | `1` | 评测采样率，取值 `0`–`1`。`1` 全部评测，`0` 全部跳过 |

配置页用更直观的文本编辑该字段，每行一条：

```
42 = on, 1.0      # 账号 42 全量评测
43 = off          # 账号 43 完全跳过
44 = on, 0.2      # 账号 44 按 20% 采样
```

## `iq_api` —— 评分接口

接口需兼容 OpenAI `/chat/completions`；插件的请求地址为 `<base_url>/chat/completions`。

| 字段 | 类型 | 默认 | 范围 | 说明 |
| --- | --- | --- | --- | --- |
| `base_url` | string | `""` | http(s) 绝对地址 | 评测接口根地址 |
| `api_key` | string | `""` | — | 作为 `Authorization: Bearer` 发送；留空则不发送该头 |
| `model` | string | `swift-1.5-iq3_xxs` | 非空 | 评分模型名 |
| `timeout_seconds` | int | `30` | 1–600 | 单次评分请求超时 |
| `max_retries` | int | `2` | 0–5 | 失败重试次数（指数退避 200ms、400ms…） |

评分请求固定 `temperature=0`、`max_tokens=512`、`stream=false`，以保证同一对话评分可复现。
仅网络抖动、`429` 与 `5xx` 会重试；其余 `4xx` 视为不可重试错误。

## `loop_detection` —— 防自评循环

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 防循环总开关 |
| `check_host` | bool | `true` | 比对请求 host 与 `iq_api.base_url` 的 host |
| `check_model` | bool | `false` | 保留字段，**当前实现恒不生效**，见下 |

> **为什么 `check_model` 不生效**：判定 model 需要预读请求体，而这会让 SDK 认为请求已开始
> 写向上游，从而把「拨号失败」误报成 `request_sent=true`，导致宿主放弃换号重试。
> 该字段保留仅为兼容旧配置，请保持 `false`。

**`check_host` 必须开启**。若评测接口指向同一个 Sub2API 实例，缺少该检查会导致
插件评测自己的评测请求，无限递归直至耗尽资源。

## `queue` —— 评测队列

转发路径不得因评测慢而堆积协程，因此评测任务进入有界队列。

| 字段 | 类型 | 默认 | 范围 | 说明 |
| --- | --- | --- | --- | --- |
| `worker_count` | int | `4` | 1–64 | 并发评分协程数 |
| `max_size` | int | `256` | 1–10000 | 待评测队列容量 |
| `drop_when_full` | bool | `true` | — | 队列满时丢弃新任务 |

`drop_when_full=false` 时队列满会等待至多 200ms，仍然满则放弃。
**建议保持 `true`**，避免评测队列反向拖慢宿主转发。

## `capture` —— 采集与留存

| 字段 | 类型 | 默认 | 范围 | 说明 |
| --- | --- | --- | --- | --- |
| `max_request_body_bytes` | int | `262144` | 1024–4194304 | 请求体采集上限 |
| `max_response_body_bytes` | int | `262144` | 1024–4194304 | 响应体采集上限 |
| `max_records_per_account` | int | `200` | 0–10000 | 每账号保留记录条数；`0` 表示不限 |
| `record_ttl_hours` | int | `0` | 0–2160 | 记录存活小时数；`0` 表示不过期 |

超过采集上限的报文会被截断并**跳过评测**（避免把残缺对话送去打分），
但正常转发不受影响。`record_ttl_hours` 上限 2160 小时（90 天）来自宿主 KV 的 TTL 限制。

## 校验失败示例

校验失败时宿主会把错误消息展示给管理员。常见情形：

| 配置 | 错误 |
| --- | --- |
| `{"enabled": true}` | 启用评测时 iq_api.base_url 不能为空 |
| `{"iq_api": {"base_url": "ftp://x"}}` | iq_api.base_url 必须是 http(s) 绝对地址 |
| `{"accounts": {"1": {"sample_ratio": 1.5}}}` | accounts[1].sample_ratio 必须在 0 到 1 之间 |
| `{"capture": {"record_ttl_hours": 99999}}` | capture.record_ttl_hours 必须在 0 到 2160 之间 |
| `{"__unknown__": 1}` | 配置解析失败：unknown field |
