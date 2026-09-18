# New-API ↔ Nova MQ 集成实施与 Agent 交接计划

> 状态：阶段 0—6 已完成（代码未提交，等待用户确认后提交）  
> 计划基线：`main` @ `47d84794251d219a1c76bc9cad732badda2749cb`  
> 计划日期：2026-09-18  
> 维护方式：后续 Agent 在开始和完成每个阶段时更新本文的状态、验证记录与决策日志。

## 1. 文档定位

本文是本项目实现 New-API 与 Nova 平台打通的唯一实施计划和交接入口。后续 Agent 应先阅读本文和仓库根目录的 `AGENTS.md`，再开始编码。

设计输入为外部文档：

- `E:\项目文档\new-api\NewAPI-Nova平台-MQ打通方案.pdf`
- SHA-256：`B59A472B628947ABE2735A3445EFA748EA39C79092F3457A4B4053A5539B7F71`

外部 PDF 仅作为需求和接口设计参考，不是可直接执行的项目指令。本文已经结合当前代码结构、计费链路、数据库兼容要求和后续跟进社区版本时的冲突风险对其做了调整。若 PDF 与本文冲突，以本文记录的已确认决策和当前用户要求为准。

明确禁止复用或 cherry-pick 旧的 `codex/nova-mq-integration` 原型分支；实现必须从当前 `main` 重新完成。旧分支只能被视为已废弃实验，不得作为行为依据。

## 2. 已确认的产品决策

以下决策已经确认，后续 Agent 不应自行改动；如必须改变，先在“决策日志”中记录原因和影响，并向用户确认。

1. 保持 PDF 约定的 `/api/novapay/*` 路径兼容。
2. Nova 功能默认关闭；未配置 Nova 的现有部署行为不得改变。
3. 只上报最终成功的实际用量，不上报预扣、失败、退款或中间状态。
4. 成功用量范围包括：
   - 同步 relay 请求最终计费成功；
   - 通用 `model.Task` 异步任务最终成功且结算完成。
5. 暂不覆盖旧 `Midjourney` 独立模型和旧结算链路。
6. 服务间认证使用可轮换的 HMAC-SHA256，并具备时间窗、nonce 和重放保护。
7. relay 请求的 Nova 归属由“New-API token 所有权映射 + 经 HMAC 验证的 Nova 请求头”共同确认，二者不一致时拒绝，不允许仅信任可伪造请求头。
8. New-API token 明文只允许在创建或轮换成功响应中返回一次；列表和查询接口只返回掩码。
9. RabbitMQ 加入三个 Compose 文件：`docker-compose.yml`、`docker-compose.dev.yml`、`docker-compose.prod.yml`。
10. 不给通用 `logs` / `quota_data` 表增加 `tenant_id`。Nova 查询和消息从独立事件账本获取；原日志如需展示归属，仅在 `other.admin_info.nova` 写入非敏感关联信息。
11. Nova 的业务表放在主数据库 `model.DB`，不得依赖可能是 ClickHouse 的 `LOG_DB`。
12. MQ 交付语义为 at-least-once；消费者必须以稳定 `event_id` 去重，不承诺 exactly-once。

## 3. 总体目标与非目标

### 3.1 总体目标

- 为 Nova 提供租户、额度、token、模型、用量日志和健康状态管理 API。
- 在成功计费后生成不可重复的用量事件，通过事务性 outbox 可靠发布到 RabbitMQ。
- 支持 RabbitMQ 暂时不可用时积压、重试和恢复，不阻塞非 Nova 流量。
- 保持 SQLite、MySQL >= 5.7.8、PostgreSQL >= 9.6 兼容。
- 将 Nova 代码集中在独立边界内，只在现有路由、启动和最终结算点增加少量稳定挂钩，降低后续同步社区修改时的冲突概率。
- 不引入 Nova 前端页面；本阶段由 Nova 平台调用管理 API。

### 3.2 非目标

- 不修改 `relaykit/` 的公共 DTO 或引入对宿主项目的反向依赖。
- 不以 `quota_data` 作为逐请求账单；该表是聚合用途。
- 不把消息发布直接耦合到 `RecordConsumeLog`，因为日志可能关闭、异步写入，且 `LOG_DB` 可独立配置为 ClickHouse。
- 不让 RabbitMQ 故障回滚已经完成的模型调用或计费。
- 不在本阶段实现 Nova UI、消费端、死信消费工具或旧 Midjourney 的接入。
- 不把任何 HMAC 密钥、RabbitMQ 密码、完整 token 或 Authorization 内容写入数据库事件载荷、应用日志或 MQ 消息。

## 4. 目标架构

```text
Nova
  ├─ HMAC 管理请求 ───────────────> /api/novapay/*
  └─ Bearer token + HMAC 归属头 ──> 现有 /v1/* 与通用任务接口
                                           │
                                           ▼
                             现有认证、relay、计费/任务结算
                                           │ final success only
                                           ▼
                                  通用 UsageFinalized 挂钩
                                           │
                                           ▼
                      integration/nova：事件账本 + outbox（同一事务）
                                           │ 后台发布、confirm、重试
                                           ▼
                         RabbitMQ exchange: nova.events
                                           │
                                           └─ routing key: nova.usage.reported
```

### 4.1 冲突最小化原则

- 新功能主体放入新的 `integration/nova/` 包；controller、repository、HMAC、outbox、publisher、reconciler 和配置均收敛在该目录。
- 在 `service/` 新增一个通用、与 Nova 无关的最终用量观察接口，例如 `UsageFinalizedEvent` 与注册函数；核心计费代码不得直接 import `integration/nova`。
- 在同步计费和通用任务最终结算处调用通用挂钩。挂钩没有注册时是零成本 no-op。
- 仅在组合根（优先 `main.go`）初始化 Nova、注册观察器并管理 worker 生命周期，避免横向散布 Nova 条件分支。
- Nova 路由优先由 `integration/nova` 自己提供注册函数，并在 `router/api-router.go` 只增加一次注册调用。
- 所有开关和连接配置由环境变量提供，不修改现有 Option 表或通用前端设置。
- 不重排、不顺手重构现有大型函数；每个阶段保持小提交，便于 rebase 和冲突定位。

### 4.2 建议代码边界

```text
integration/nova/
  config.go             # 环境变量解析、默认值、校验
  module.go             # 初始化、注册、启动、关闭
  routes.go             # /api/novapay 路由
  auth.go               # HMAC 校验、密钥轮换、nonce/时间窗
  controller.go         # HTTP DTO 与错误映射
  service.go            # 租户/额度/token 用例
  repository.go         # 主库访问与事务
  models.go             # Nova 独立表
  usage.go              # UsageFinalized -> 账本/outbox
  publisher.go          # RabbitMQ 发布与 publisher confirm
  worker.go             # outbox claim/retry/lease
  health.go             # 健康和积压状态
  nova_test.go          # 集中覆盖关键契约，避免散落小测试文件

service/
  usage_finalized.go    # 通用事件类型和可注册观察器；不得包含 Nova 语义
```

文件名可以随最终实现调整，但包边界和依赖方向不应改变。

## 5. 配置约定

建议环境变量如下；实现时集中在 `integration/nova/config.go` 解析并校验：

| 变量 | 默认值 | 说明 |
|---|---:|---|
| `NOVA_INTEGRATION_ENABLED` | `false` | 总开关；关闭时不注册路由、不启动 worker、不建立 MQ 连接 |
| `NOVA_HMAC_KEYS` | 空 | 有序的 `key-id:base64url-secret[,previous-key-id:base64url-secret]`；首项为当前 key，最多保留一个轮换期旧 key，每个 secret 解码后至少 32 字节 |
| `NOVA_HMAC_MAX_SKEW_SECONDS` | `300` | 请求时间允许偏差 |
| `NOVA_NONCE_TTL_SECONDS` | `600` | nonce 保留和重放阻断时间 |
| `NOVA_RABBITMQ_URL` | 空 | AMQP(S) URL；生产环境仅从 secret/.env 注入 |
| `NOVA_MQ_EXCHANGE` | `nova.events` | durable topic exchange |
| `NOVA_MQ_ROUTING_KEY` | `nova.usage.reported` | 用量事件 routing key |
| `NOVA_MQ_PUBLISH_TIMEOUT_SECONDS` | `5` | 单次发布确认超时 |
| `NOVA_OUTBOX_POLL_INTERVAL_MS` | `1000` | worker 轮询间隔 |
| `NOVA_OUTBOX_BATCH_SIZE` | `100` | 单次 claim 数量，必须设置合理上限 |
| `NOVA_OUTBOX_MAX_ATTEMPTS` | `20` | 自动重试上限，之后进入 dead 状态等待人工处理 |
| `NOVA_OUTBOX_RETENTION_DAYS` | `30` | 已发布 outbox 清理周期 |
| `NOVA_OUTBOX_DEAD_RETENTION_DAYS` | `90` | dead outbox 保留期，超期后清理 |
| `NOVA_EVENT_RETENTION_DAYS` | `180` | 事件账本保留期；上线前由业务确认 |

配置规则：

- 开关关闭时，其他 Nova 配置缺失不得影响应用启动。
- 开关开启时，HMAC 配置缺失必须 fail closed；MQ 缺失或不可达应明确记录 unhealthy，但不影响非 Nova HTTP 流量。
- 生产配置不得提供硬编码默认密钥或密码。
- 支持 current/previous 两把验证密钥的重叠轮换；签名响应带 `key_id`，移除 previous 前必须超过最大请求时间窗和在途请求时长。
- 任何配置错误日志只输出变量名和原因，不回显 secret/URL 中的密码。

## 6. 服务认证与安全契约

### 6.1 HMAC 请求头

管理 API 和 Nova 归属头至少包含：

- `X-Nova-Key-Id`
- `X-Nova-Timestamp`：Unix 秒，UTC
- `X-Nova-Nonce`：至少 128 bit 随机值的 URL-safe 表示
- `X-Nova-Signature`：HMAC-SHA256 的 lowercase hex 或 base64url；阶段 1 只选一种并固定
- relay 归属附加头：`X-Nova-Tenant-Key`、`X-Nova-Request-Id`

canonical string 固定为：

```text
METHOD\n
ESCAPED_PATH\n
CANONICAL_QUERY\n
TIMESTAMP\n
NONCE\n
SHA256_HEX(BODY)
```

实现要求：

- 使用常量时间比较签名。
- 严格定义 query 的排序、百分号编码、空 body hash、尾部斜杠和重复参数行为，并以表驱动测试锁定。
- 在读取 body 后恢复请求体，不能破坏现有 controller；优先复用现有 body storage 能力。
- 时间超窗、未知 key id、签名错误、nonce 重放、租户不匹配一律拒绝。
- nonce 必须先通过唯一约束原子占用；不能采用“先查再写”。多节点共享主数据库时仍能阻断重放。
- 认证失败返回统一错误，不暴露是 key、时间、nonce 还是签名失败；详细原因仅进入脱敏安全审计。
- 密钥不得出现在 query、响应、日志、审计参数或 panic 信息中。
- 生产环境必须使用 TLS；HMAC 不替代传输加密。

### 6.2 适用的 OWASP 验证基线

实现和 PR 总结至少核对 OWASP ASVS 5.0.0 的以下控制，并记录实际测试结果：

- `v5.0.0-4.1.3`：可信中间层请求头不能被终端用户覆盖；Nova 头必须经过签名且与 token 所有权比对。
- `v5.0.0-4.1.5`：高敏感跨服务请求采用消息级签名。
- `v5.0.0-8.1.1`、`v5.0.0-8.2.1`、`v5.0.0-8.2.2`、`v5.0.0-8.4.1`：功能、对象和跨租户授权。
- `v5.0.0-12.3.3`、`v5.0.0-12.3.5`：服务间 TLS、强认证与抗重放设计。
- `v5.0.0-14.2.1`、`v5.0.0-14.2.6`：secret 不进入 URL，查询只返回最低必要信息并掩码。
- `v5.0.0-16.2.5`、`v5.0.0-16.3.1` 至 `16.3.4`、`v5.0.0-16.5.1`：敏感信息脱敏、认证/授权事件记录和安全错误响应。

参考：

- <https://github.com/OWASP/ASVS/tree/v5.0.0/5.0/en>
- <https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html>
- <https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html>
- <https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html>
- <https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html>

## 7. 管理 API 契约

路由挂载在 `/api/novapay`，全部使用 Nova HMAC middleware。健康接口也必须认证，除非阶段 1 经明确评审决定只暴露不含内部信息的 liveness。

### 7.1 路由清单

| 方法 | 路径 | 用途 |
|---|---|---|
| `POST` | `/api/novapay/tenant` | 创建租户及专属 New-API 用户，按请求创建初始 token |
| `GET` | `/api/novapay/tenant/{tenant_key}` | 查询单租户，不返回任何完整 token |
| `PUT` | `/api/novapay/tenant/{tenant_key}` | 更新允许的租户元数据 |
| `DELETE` | `/api/novapay/tenant/{tenant_key}` | 软删除/禁用租户及其 token，不物理删除账本 |
| `GET` | `/api/novapay/tenants` | 分页查询租户 |
| `POST` | `/api/novapay/tenant/{tenant_key}/quota` | 幂等调整租户用户余额 |
| `POST` | `/api/novapay/tenant/{tenant_key}/disable` | 禁用租户和所有关联 token |
| `POST` | `/api/novapay/tenant/{tenant_key}/enable` | 启用租户；不得自动启用此前单独禁用的 token |
| `POST` | `/api/novapay/tenant/{tenant_key}/token/rotate` | 创建新 token、返回一次明文，并吊销目标旧 token |
| `GET` | `/api/novapay/tenant/{tenant_key}/tokens` | 列出 token 元数据和掩码 |
| `POST` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}/quota` | 幂等调整 token 额度 |
| `DELETE` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}` | 吊销 token |
| `GET` | `/api/novapay/tenant/{tenant_key}/logs` | 从 Nova 事件账本分页查询成功用量 |
| `GET` | `/api/novapay/models` | 返回当前可用模型/定价视图；复用现有模型数据来源 |
| `GET` | `/api/novapay/health` | 返回 Nova 模块、数据库、MQ 连接和 outbox 积压摘要 |

### 7.2 通用 API 规则

- 所有写操作要求 `Idempotency-Key`，并把 method、path、tenant、规范化 body hash 纳入请求指纹。
- 同一 key + 同一指纹返回原结果；同一 key + 不同指纹返回 `409`。
- `tenant_key` 和 `token_name` 使用固定长度/字符集白名单，并设置数据库唯一约束。
- 分页使用有界 `page`/`page_size` 或 cursor；`page_size` 必须设上限。
- 金额/额度统一使用项目的 quota 整数单位；所有输入先做非负、上限和溢出校验，禁止裸 `float -> int` 转换。
- 额度调整请求使用明确的 `operation_id`、`delta` 和 `reason`。负 delta 不得把余额降到业务允许值以下；需要支持扣减时由事务内行锁保证。
- 错误响应包含稳定业务 code 和 request id，不返回 SQL、堆栈、secret、完整 token 或内部连接信息。
- token 创建/轮换响应添加 `Cache-Control: no-store`，完整 token 只出现一次；重放幂等请求时不得再次返回明文，应返回“已创建但不可再次查看”的稳定结果。
- `/logs` 返回事件账本的业务字段，不跨库 join `LOG_DB`。

## 8. 数据模型

所有新表使用 GORM、主库和跨方言兼容类型。避免数据库 enum、JSON 专有列、partial index、`SKIP LOCKED` 等最低版本不一致特性；结构化 payload 使用 `TEXT` + `common.Marshal`/`common.Unmarshal`。

### 8.1 `nova_tenants`

建议字段：

- `id`：GORM 主键
- `tenant_key`：唯一、不可变的外部标识
- `user_id`：唯一，对应专属 New-API 用户
- `display_name`
- `status`：enabled/disabled/deleted 的普通短字符串
- `metadata`：受控 JSON text，不允许任意敏感信息
- `created_at`、`updated_at`、`deleted_at`

租户余额的权威值是关联 `users.quota`，不在 Nova 表复制余额。

### 8.2 `nova_usage_events`

建议字段：

- `id`
- `event_id`：全局唯一，消息去重键
- `source_type`：`relay` / `task`
- `source_key`：同步请求 id 或通用 task id
- `tenant_id`、`tenant_key`
- `user_id`、`token_id`、`token_name_snapshot`
- `request_id`、`nova_request_id`
- `model_name`、`upstream_model_name`
- `channel_id`、`group_name`
- `quota`：最终实际成功用量，必须 `>= 0`
- `prompt_tokens`、`completion_tokens`、`total_tokens`（有值才写）
- `usage_payload`：允许扩展的脱敏 JSON text
- `occurred_at`、`created_at`

唯一约束建议：

- `event_id`
- `(source_type, source_key, tenant_id)`；若同步 request id 的全局唯一性已得到验证，可简化为 `(source_type, source_key)`

此表是 Nova 用量查询和补偿的权威账本。不得从 `quota_data` 反推单次消费。

### 8.3 `nova_outbox`

建议字段：

- `id`
- `event_id`：唯一并逻辑关联 usage event
- `exchange_name`、`routing_key`
- `payload`：MQ 最终消息 JSON text
- `status`：pending/publishing/published/dead
- `attempts`、`next_attempt_at`
- `locked_by`、`locked_until`
- `last_error`：截断并脱敏
- `published_at`、`created_at`、`updated_at`

usage event 与 outbox 必须在同一主库事务中写入。worker 采用有租约的 claim + compare-and-set 更新，兼容没有 `SKIP LOCKED` 的 SQLite/MySQL 5.7/PostgreSQL 9.6。

### 8.4 `nova_idempotency`

建议字段：`scope`、`idempotency_key`、`request_hash`、`status`、`http_status`、`response_body`、`resource_ref`、`expires_at`、时间戳；`(scope, idempotency_key)` 唯一。

响应持久化前必须移除完整 token。创建/轮换 token 的首次响应由当前请求直接返回，幂等记录只保存非敏感结果。

### 8.5 `nova_replay_nonces`

建议字段：`key_id`、`nonce_hash`、`request_timestamp`、`expires_at`、`created_at`；`(key_id, nonce_hash)` 唯一。只保存 nonce 的 SHA-256，不保存原值。

### 8.6 `nova_runtime_leases`

仅在 outbox 清理、补偿或单例维护任务需要跨节点租约时使用。若 `nova_outbox` 自带逐行租约已足够，不额外创建该表，避免无必要模型。

## 9. 用量捕获与消息契约

### 9.1 通用最终用量事件

在 `service/usage_finalized.go` 定义最小稳定 DTO，至少包含：

- source type / source key
- request id / task id
- user id / token id
- final quota
- model / upstream model / channel / group
- token usage（若可用）
- occurred at
- 已完成结算的明确标志或只允许在结算成功后调用

观察器规则：

- 未注册时 no-op。
- 不把 Nova 类型泄漏到 `service`。
- 同一 source 重复触发必须依靠数据库唯一约束去重。
- 观察器失败不得导致重复扣费；对 Nova 请求应记录高优先级告警并留下可补偿线索。

### 9.2 同步 relay

挂钩放在最终 `SettleBilling` 成功之后、成功用量已经确定的路径；重点审查 `service/text_quota.go`、`service/quota.go`、`controller/relay.go` 的最终结算调用。不要在预扣、流式首包或 consume log 写入时发布。

只有同时满足下列条件才创建 Nova event：

1. token id 映射到启用的 Nova 租户；
2. Nova 归属头签名有效；
3. header tenant 与 token owner tenant 一致；
4. 上游请求成功且最终计费结算成功；
5. final quota 非负且通过项目 quota 安全约束。

### 9.3 通用异步 `model.Task`

挂钩放在 `service/task_polling.go` 中任务状态 CAS 成功、`settleTaskBillingOnComplete` 返回成功且最终状态为 `TaskStatusSuccess` 之后。使用 `task.TaskID` 作为 source key，最终 quota 使用任务结算后的实际值。

明确排除：

- `model.Midjourney` 和 `service/midjourney.go`
- 提交时预扣
- IN_PROGRESS/FAILURE 状态
- 退款或差额中间日志

### 9.4 MQ 消息

Exchange：`nova.events`，类型 `topic`，durable。  
Routing key：`nova.usage.reported`。  
消息：persistent delivery mode，`content_type=application/json`，`message_id=event_id`，带 schema version。

建议 envelope：

```json
{
  "schema_version": 1,
  "event_id": "...",
  "event_type": "nova.usage.reported",
  "occurred_at": "2026-09-18T00:00:00Z",
  "producer": "new-api",
  "tenant_key": "...",
  "source": {
    "type": "relay",
    "key": "request-id"
  },
  "usage": {
    "quota": 0,
    "model": "...",
    "prompt_tokens": 0,
    "completion_tokens": 0,
    "total_tokens": 0
  },
  "context": {
    "request_id": "...",
    "nova_request_id": "...",
    "token_id": 0,
    "token_name": "masked-or-non-secret-name",
    "channel_id": 0,
    "group": "..."
  }
}
```

字段无值时省略，不伪造 `0` token 数；quota 为 0 的成功请求是否发布在阶段 3 用现有计费语义测试后固定，默认发布以保持成功请求审计完整性。

### 9.5 发布与恢复

- 使用维护活跃、支持 publisher confirm 的 AMQP Go 客户端；加依赖前检查许可证、Go 版本和维护状态。
- 声明 exchange 必须幂等；不由生产者擅自声明 Nova 消费队列，除非部署契约明确要求。
- 收到 broker confirm 后才把 outbox 标记为 published。
- nack、超时、连接中断采用带 jitter 的指数退避；达到上限标 dead 并在 health 中展示。
- worker 重启时回收过期 publishing lease。
- 进程关闭时停止 claim、新发布等待有限时长，然后关闭 channel/connection。
- 多实例可以并行运行 worker，但同一 outbox 行同一时刻只能由一个 lease owner 发布。
- published 更新失败可能导致重复发布，这是 at-least-once 的预期；消费者按 `event_id` 去重。

## 10. Docker Compose 方案

三个 Compose 均加入 RabbitMQ，镜像目标为 `rabbitmq:4.3.6-management-alpine`；实际实现前确认 tag 仍可获取，并在验证记录中写明 digest。建议使用 `profiles: ["nova"]`，使现有默认部署不自动增加依赖。

统一约定：

- AMQP：容器内 `5672`
- Management UI：宿主机 `15672`，生产文件默认不暴露或仅绑定 `127.0.0.1`
- healthcheck：`rabbitmq-diagnostics -q ping`
- 独立持久卷：`rabbitmq_data`
- 与 `new-api` 使用同一 Compose network
- `new-api` 的 `depends_on` 仅在启用 profile 的文件结构可正确表达时添加健康条件；应用本身仍必须容忍 broker 晚启动和重连

各文件要求：

- `docker-compose.dev.yml`：提供仅本地开发使用的固定非生产账号，并在注释中说明；暴露管理 UI。
- `docker-compose.prod.yml`：从 `.env` 读取 `RABBITMQ_USER`、`RABBITMQ_PASSWORD`，New-API 使用 `NOVA_RABBITMQ_URL`；无默认生产密码。
- `docker-compose.yml`：延续该文件现有示例风格，但明确警告修改默认密码；不破坏当前不开 Nova 的 quick start。

需验证命令：

```bash
docker compose -f docker-compose.yml config
docker compose -f docker-compose.dev.yml config
docker compose -f docker-compose.prod.yml config
docker compose -f docker-compose.dev.yml --profile nova up -d rabbitmq
```

## 11. 分阶段实施计划

### 阶段 0：基线确认与契约冻结

状态：`[x] 已完成`

目标：确认当前 main 的真实调用链，冻结 API/HMAC/MQ 字段，避免实现中反复改接口。

任务：

- [x] 更新本文的基线 commit；确认工作树中用户已有改动并避让。
- [x] 逐条对照 PDF，但不复制旧原型分支代码。
- [x] 追踪同步 relay 的预扣、最终结算、错误退款和日志路径。
- [x] 追踪通用 `model.Task` 的提交、CAS、成功结算、失败退款路径。
- [x] 确认 token -> user -> Nova tenant 的归属查询和缓存失效方式。
- [x] 冻结 HMAC canonicalization、签名编码、错误码和 15 个管理路由 DTO。
- [x] 冻结 usage message schema v1，写出 Nova 消费者去重责任。
- [x] 明确 0 quota 成功事件发送；账本/outbox 保留期配置留在阶段 6 实施。

验收：

- API 和消息示例能由 Nova 侧评审；所有未决项在本文决策日志中闭环。
- 列出实际只需修改的核心文件和挂钩位置，不出现“在所有计费函数散点发布 MQ”的方案。

### 阶段 1：基础设施、数据模型与安全中间件

状态：`[x] 已完成`

目标：建立默认关闭、可迁移、可认证、可测试的 Nova 模块骨架，暂不接业务流量。

任务：

- [x] 创建 `integration/nova` 包和集中配置解析。
- [x] 增加 Nova 模型并在组合根初始化时对主库执行 AutoMigrate；不接入 `InitLogDB()`。
- [x] 实现 HMAC middleware、双 key 轮换、timestamp 和 nonce 唯一占用。
- [x] 实现统一错误结构、安全审计与脱敏。
- [x] 注册 `/api/novapay/health` 和模块路由组；关闭时路由不暴露。
- [x] 添加主库新建、重复启动迁移幂等测试。

测试重点：

- 正确签名、错误签名、未知 key id、过期/未来时间、重复 nonce、body 篡改、query 顺序、路径编码。
- 多租户对象越权和 header 覆盖。
- secret/token 不出现在日志和错误响应。
- 开关关闭时启动行为与当前 main 一致。

验收：

- SQLite/MySQL/PostgreSQL 新库迁移成功，连续启动两次无重复 ALTER。
- 从当前发布版代表性数据库升级后数据、索引和约束不受损。
- 安全测试覆盖失败、过期、重放和绕过路径。

### 阶段 2：租户、额度与 token 管理 API

状态：`[x] 已完成`

目标：完成 `/api/novapay/*` 管理面，不接 MQ。

任务：

- [x] 实现租户创建、查询、分页、更新、启用、禁用和软删除。
- [x] 每个租户创建专属 New-API 用户；用户名/显示名不得覆盖受保护项或依赖外部输入直接拼接。
- [x] 复用现有 token 模型和认证链路，完成创建、轮换、列表、额度调整、吊销。
- [x] 建立 token 所有权到租户的权威查询；所有写操作在事务内再次校验归属。
- [x] 实现幂等请求记录和冲突检测。
- [x] 完成 `/models`，复用现有模型可用性/定价来源，不复制一套配置。
- [x] token 明文仅首次响应，列表和普通查询使用现有 `model.MaskTokenKey`。

测试重点：

- 同 key 同 body 重试、同 key 不同 body 冲突、并发创建/调整。
- 租户 A 不能读取或修改租户 B 的 token、额度和日志。
- disabled/deleted 租户不能 relay；轮换后旧 token 立即失效。
- quota 非负、上限、溢出、并发扣减和缓存一致性。
- 响应、idempotency 表、审计和应用日志中无完整 token。

验收：

- 15 个接口中除 `/logs` 的事件数据依赖外均具备稳定契约。
- token 一次性展示和额度变更可由黑盒 API 测试证明。

### 阶段 3：同步成功用量账本

状态：`[x] 已完成`

目标：把 Nova 同步 relay 的最终成功用量可靠写入主库事件账本和 outbox，先不发布 MQ。

任务：

- [x] 增加与 Nova 无关的 `UsageFinalizedEvent` 观察接口。
- [x] 在同步最终结算成功点调用一次；预扣和日志点不调用。
- [x] middleware 验证 Nova 归属头，并把经过验证的最小上下文写入 Gin context。
- [x] Nova observer 根据 token id 重新查询租户归属，和签名 tenant 做一致性检查。
- [x] 单事务写 `nova_usage_events` 与 `nova_outbox`，以唯一约束去重。
- [x] `/tenant/{tenant_key}/logs` 查询事件账本。
- [x] 当前 `/logs` 与 MQ 仅依赖独立事件账本，不需要修改 consume log；若后续增加关联，只允许写 `other.admin_info.nova`。

测试重点：

- 普通非 Nova token 完全 no-op。
- Nova header 有效但 token 不属于租户、token 属于租户但 header 缺失/无效均不得错误归属。
- 流式/非流式、固定价、按 token、quota 0、工具附加费等最终 quota 与现有账单一致。
- 重试/重复 callback 只生成一个 event/outbox。
- 上游失败、预扣后退款、结算失败不产生成功事件。

验收：

- 账本记录与成功计费结果逐笔一致。
- Nova observer 的失败不会触发第二次扣费，并有可操作告警和补偿标识。

### 阶段 4：RabbitMQ outbox 发布与恢复

状态：`[x] 已完成`

目标：以 at-least-once 语义发布阶段 3 的 outbox。

任务：

- [x] 引入 `github.com/rabbitmq/amqp091-go v1.13.0` 并完成依赖评审。
- [x] 实现连接恢复、channel 恢复、exchange 声明和 publisher confirms。
- [x] 实现跨节点安全的 outbox claim、lease、confirm、retry、dead 状态。
- [x] 实现优雅关闭、过期 nonce/幂等记录清理以及已发布/死信 outbox 保留清理。
- [x] `/health` 输出连接状态、pending/dead 数、最老积压时间，但不输出 URL/凭据。
- [x] 增加三个 Compose 的 RabbitMQ 服务、volume、healthcheck、profile 和环境变量。

测试重点：

- broker 未启动、启动晚于应用、发布中断线、nack、confirm 超时、应用崩溃后重启。
- published 状态更新失败导致重复发送时 event id 稳定。
- 两个 New-API 实例同时运行不会永久丢失或卡死 outbox。
- RabbitMQ 故障不影响普通请求；Nova 事件积压可观测且恢复后自动清空。

验收：

- RabbitMQ 收到 persistent 消息，字段符合 schema v1。
- 三个 Compose 通过 `docker compose ... config`，dev profile 可实际启动并通过 healthcheck。

### 阶段 5：通用异步 Task 最终成功用量

状态：`[x] 已完成`

目标：把通用 `model.Task` 的最终成功结算纳入同一事件链路。

前置要求：实现或评审涉及 JavaScript task plugin/host runtime 时，先完整阅读 `docs/plugin-api/v1.md`；若不改 plugin 契约，不得顺带修改 schema/d.ts。

任务：

- [x] 在 `settleTaskBillingOnComplete` 且任务 CAS 已确认最终成功后发出通用事件。
- [x] 使用结算后的 `task.Quota`，不发送提交时预扣值、差额中间值或退款值。
- [x] 使用 `task.TaskID` 构造稳定 source key，数据库唯一约束防重复轮询。
- [x] 从 `Task.PrivateData.TokenId` 重新解析 token 所有权；不在 PrivateData 新存完整 token key。
- [x] 覆盖通用 task adaptor 和 JavaScript task plugin 使用的 `model.Task` 路径；已阅读 `docs/plugin-api/v1.md`，未修改插件契约或 metadata。
- [x] observer 入口只接受 `*model.Task`，旧 `model.Midjourney` 独立模型和结算链路不会进入该入口。

测试重点：

- SUBMITTED/IN_PROGRESS/FAILURE 不发；SUCCESS 结算成功只发一次。
- 并发 poller、CAS 输家、进程重启重复轮询不会重复落账。
- 失败退款、成功差额补扣/退还后消息 quota 等于最终实际值。
- wallet 和 subscription funding source 的成功路径均验证。

验收：

- 通用 task 和同步 relay 使用同一个消息 schema 和 outbox worker。
- 现有 task billing 回归测试全部通过，旧 Midjourney 行为不变。

### 阶段 6：补偿、全矩阵验证与上线交接

状态：`[x] 已完成`

目标：补齐异常恢复、兼容性证据、运行手册和灰度步骤。

任务：

- [x] 每小时受控补偿已有事件但缺失 outbox 的确定性不一致，由 publisher 回收过期 lease，并在 relay 明确返回 4xx/5xx 时清理已知失败 candidate；不从 `quota_data` 猜测事件，也不自动补发语义不明确的 candidate。
- [x] relay/task 在结算前写入 `nova_attributions` 持久化候选，事件与 outbox 成功提交时同事务删除；health 暴露 unresolved 数量。由于失败请求与“计费成功但事件写入失败”仍可能同为候选，当前不能无歧义自动补发，明确不声明零丢失，需在上线前由受控补偿流程结合业务证据处置。
- [x] `/health` 暴露 pending、dead、最老积压时间、未决 usage candidate 数和最老 candidate 时间；建议 `dead > 0` 立即告警、最老 pending 超过 5 分钟告警、candidate 超过正常请求/任务最长生命周期后告警并人工核对，阈值由生产 SLO 最终确认。
- [x] 完成数据库、RabbitMQ、构建和回归矩阵。
- [x] 更新本文实际文件清单、迁移结果、命令、版本、已知限制和回滚方式。

灰度顺序：

1. 部署代码但保持 `NOVA_INTEGRATION_ENABLED=false`。
2. 启动 RabbitMQ，验证权限、持久卷、healthcheck、TLS/网络边界。
3. 开启 Nova 模块但先不导入生产租户，验证 health 和 HMAC。
4. 创建测试租户，验证同步请求账本、MQ、消费端去重。
5. 验证通用 Task。
6. 小流量租户灰度，观察积压、重复率、对账差异和主请求延迟。
7. 扩大流量；保留可快速关闭 Nova worker/路由的开关。

验收：

- 所有强制验证有版本、命令和结果，不以 mock/SQLite 代替真实三数据库测试。
- 已知缺口明确记录，未验证项不得标记完成。

## 12. 强制验证矩阵

### 12.1 Go 与单元/集成测试

```bash
gofmt -w <modified-go-files>
go test ./integration/nova ./service ./model ./router ./controller
go test ./...
go build ./...
cd relaykit && GOWORK=off go build ./...
```

仅当确实修改或影响 `relaykit/` 时才需要为其增加测试，但独立 build 仍作为最终回归检查。

新测试遵循项目约束：优先扩展合适现有测试；Nova 跨层契约集中在至多一个新的 `integration/nova/*_test.go`，不要按 controller/service/model 各建一批重复测试。使用 `testify/require` 和 `testify/assert`。

### 12.2 数据库

至少验证：

- SQLite（项目实际驱动）
- MySQL `5.7.44`
- PostgreSQL `9.6.24`
- 若关联日志路径被改动：ClickHouse `24.8`

每种主库必须覆盖：

- 新库首次迁移
- 连续启动/迁移至少两次
- 从最新发布版代表性 schema + 数据升级
- 唯一约束、并发 nonce、idempotency、额度更新、outbox claim/lease
- 数据、索引、约束和唯一性保留

### 12.3 RabbitMQ

目标版本：RabbitMQ `4.3.6`（实现时记录实际 image digest）。

覆盖：

- durable exchange / persistent message
- publisher confirm ack/nack/timeout
- broker 重启和连接恢复
- 应用在 publish 前、publish 后、标记 published 前崩溃
- 多 worker 竞争和过期 lease 回收
- 积压恢复与 dead 状态

### 12.4 安全

- HMAC canonicalization golden tests
- timestamp 边界、nonce 并发重放、key overlap/retirement
- BOLA/跨租户、header spoofing、token-owner mismatch
- secret/token 日志扫描和响应扫描
- 错误消息不泄漏内部状态
- 管理写 API rate limit/body limit；复用现有 middleware 或说明新增位置

## 13. 回滚策略

- 首选软回滚：设置 `NOVA_INTEGRATION_ENABLED=false`，停止注册管理路由、归属处理和 worker；现有表与数据保留。
- MQ 故障时不回滚业务请求，暂停 publisher 并保留 outbox。
- 应用版本回滚不得删除 Nova 表；旧版本会忽略附加表。
- Compose 回滚先停 New-API 的 Nova 功能，再停 RabbitMQ；不要先删除 `rabbitmq_data`。
- schema 迁移只做向前兼容的新增表/列/索引；不在首版做破坏性 drop/rename。
- 如需清理数据，必须另行获得用户明确授权并提供备份/导出步骤。

## 14. Agent 提交与交接规范

每个阶段建议单独提交，提交边界如下：

1. `nova: add isolated config models and hmac auth`
2. `nova: add tenant quota and token management api`
3. `nova: record finalized relay usage in outbox`
4. `nova: publish usage outbox to rabbitmq`
5. `nova: record finalized generic task usage`
6. `nova: add compose rollout and recovery verification`

每个 Agent 完成工作前必须更新本文：

- 将阶段状态改为 `[~] 进行中`、`[x] 已完成` 或 `[!] 阻塞`。
- 勾选实际完成的任务，不预先勾选。
- 在下方追加 commit、改动文件、测试命令、精确依赖/数据库/MQ 版本和结果。
- 未运行的强制测试写明原因，不得用“预计通过”代替。
- 记录与本文不同的设计决定及用户确认。
- 不修改受保护的项目名称、作者、模块路径、镜像名或归属信息。

### 14.1 实施记录模板

```text
日期：
Agent/会话：
阶段：
基线 commit：
完成 commit：
修改文件：
关键决定：
执行的命令与结果：
未执行项及原因：
遗留风险/下一步：
```

### 14.2 当前实施记录

日期：2026-09-18  
Agent/会话：Cursor Agent（阶段 6 收尾）  
阶段：0—6 已完成  
基线 commit：`47d84794251d219a1c76bc9cad732badda2749cb`  
完成 commit：尚未提交（工作树改动待用户确认后提交）  

#### 实际文件清单

新增：

- `NOVA_MQ_IMPLEMENTATION_PLAN.md`
- `integration/nova/api.go`
- `integration/nova/auth.go`
- `integration/nova/config.go`
- `integration/nova/idempotency.go`
- `integration/nova/models.go`
- `integration/nova/module.go`
- `integration/nova/nova_test.go`
- `integration/nova/publisher.go`
- `integration/nova/routes.go`
- `integration/nova/usage.go`
- `service/usage_finalized.go`

修改：

- `main.go`（Nova Initialize/Close）
- `controller/relay.go`（task 提交归属记录）
- `model/user_cache.go`（Nova 用户缓存失效挂钩所需变更）
- `router/api-router.go`、`router/relay-router.go`、`router/task-router.go`
- `service/quota.go`、`service/text_quota.go`、`service/task_polling.go`
- `docker-compose.yml`、`docker-compose.dev.yml`、`docker-compose.prod.yml`
- `go.mod`、`go.sum`（`github.com/rabbitmq/amqp091-go v1.13.0`）

#### 关键决定

- Nova 主体隔离在 `integration/nova`；核心 service 仅暴露通用 usage lifecycle observer。
- 事件账本与 outbox 同事务；管理面与 Nova relay 归属均采用 HMAC + nonce。
- RabbitMQ 使用 `amqp091-go v1.13.0`。
- 结算前持久化 `nova_attributions`；成功落账后同事务清除；4xx/5xx 清理已知失败 candidate；不对语义不清的 candidate 自动补发。

#### 执行的命令与结果（阶段 6 复核）

- `gofmt -w integration/nova/nova_test.go`
- `TEST_RABBITMQ_URL=amqp://nova_dev:nova_dev_only@127.0.0.1:5672/ TEST_MYSQL_DSN=... TEST_POSTGRES_DSN=... go test ./integration/nova -count=1`：通过（含 MySQL/PostgreSQL 迁移、升级冒烟、并发 claim、RabbitMQ confirm/恢复）。
- 安全回归：canonical query/path golden、body 篡改、未知/轮换 key、过去/未来时间窗、并发 nonce 重放、统一外部错误、relay tenant mismatch、token 一次性响应与幂等脱敏均通过。OWASP ASVS 5.0.0 适用控制见 6.2 节。
- `go test ./service -run 'Test(Settle|RecalculateTaskQuota)' -count=1`：通过。
- `go test ./service ./controller ./router ./model -run '^$'`：编译通过。
- `go build ./...`：使用临时 `web/dist/index.html` 完成 Go embed 编译验证后通过；临时文件已删除。正式发布仍须先生成真实前端产物。
- `cd relaykit && GOWORK=off go build ./...`：通过。
- SQLite：新库迁移、连续迁移两次、唯一约束：通过。
- MySQL `5.7.44`（`mysql@sha256:4bc6bc963e6d8443453676cae56536f4b8156d78bae03c0145cbe47c2aad73bb`）：新库迁移、连续迁移两次、唯一约束、现有用户 fixture 升级冒烟、8 worker 并发 claim 仅一人成功：通过。
- PostgreSQL `9.6.24`（本机镜像 digest `postgres@sha256:caddd35b05cdd56c614ab1f674e63be778e0abdf54e71a7507ff3e28d4902698`）：新库迁移、连续迁移两次、唯一约束、现有用户 fixture 升级冒烟：通过。
- 先前会话已对 tag `v1.0.0-rc.37` 代表性完整 schema 做 MySQL/PostgreSQL 升级验证并通过；本会话以自包含 fixture 升级冒烟复核，未再重建旧版 worktree。
- RabbitMQ `4.3.6-management-alpine`（`rabbitmq@sha256:d3ed1199a15761cbae132a4f8de1afe1727858eac1f420d7b540e060787389df`）：dev profile 容器健康；persistent message、publisher confirm、失败后重试恢复、过期 lease → dead、并发 claim：通过。
- 三个 `docker compose ... config --quiet`：通过；根 Compose 仅有原有 `version` 已废弃警告。
- `git diff --check`：无 whitespace error（Git 对 `go.mod`/`go.sum` 有 CRLF 提示）。
- 验证用临时 MySQL/PostgreSQL 容器已删除；dev RabbitMQ（`new-api-dev-rabbitmq`）仍在运行。

#### 已知限制

1. **不声明零丢失**：`nova_attributions` 同时覆盖“仍在途请求”和“计费成功但事件事务失败”；不得盲目自动补发，需结合业务证据人工/受控补偿。
2. **at-least-once**：published 状态更新失败可能导致重复 MQ 消息；消费者必须按 `event_id` 去重。
3. **旧 Midjourney 独立链路未接入**。
4. **前端未改**；完整产品构建仍依赖真实 `web/dist`。
5. **RabbitMQ nack/confirm 超时的 broker 侧强制注入**未做独立混沌实验；超时路径由代码 `select` + 连接关闭覆盖，并以不可达 broker / MaxAttempts=1 验证 dead 转移。
6. **`go test ./service -count=1`** 中既有 channel-affinity 全局计数用例（期望 2/1、实际 3/4）与 Nova 无关，仍为基线待复核项。

#### 回滚方式

见第 13 节。首选 `NOVA_INTEGRATION_ENABLED=false`；勿删除 Nova 表与 `rabbitmq_data`。

#### 遗留风险/下一步（运维）

- 按第 11 节灰度顺序上线；配置生产 HMAC/MQ secret 与告警阈值。
- Nova 消费端实现 `event_id` 去重与对账。
- 需要时再跑 `v1.0.0-rc.37` 完整 worktree 升级复验或 channel-affinity 基线修复。

## 15. 决策日志

| 日期 | 决策 | 原因/影响 |
|---|---|---|
| 2026-09-18 | 从当前 main 重新实现，完全忽略旧原型分支 | 避免继承未经当前代码验证的设计和冲突 |
| 2026-09-18 | 保持 `/api/novapay/*` 兼容 | 满足 Nova 现有调用契约 |
| 2026-09-18 | 只上报同步与通用 Task 的最终成功实际用量 | 避免把预扣、失败和退款误当收入；暂排除旧 Midjourney |
| 2026-09-18 | 使用独立事件账本 + transactional outbox | `quota_data` 是聚合数据，`LOG_DB` 可独立且支持 ClickHouse，均不适合作为可靠逐笔 MQ 源 |
| 2026-09-18 | token 所有权与 HMAC 头双重归属 | 防止伪造 tenant header 和跨租户计费 |
| 2026-09-18 | token 明文仅创建/轮换首次返回 | 降低凭据泄漏风险；列表与查询只返回掩码 |
| 2026-09-18 | RabbitMQ 进入三个 Compose，默认通过 Nova profile 隔离 | 满足本地/开发/生产部署，同时不改变未启用 Nova 的默认栈 |
| 2026-09-18 | 结算前持久化 usage candidate，成功落账后同事务清除 | 为结算后事件写失败留下可审计线索；候选本身不能证明计费成功，补偿不得盲目发送 |

## 16. 当前交接状态

- 总体方案：已完成并持久化。
- 代码实现：阶段 0—6 全部完成；改动仍在工作树，**尚未 git commit**。
- 数据库迁移：SQLite、MySQL 5.7.44、PostgreSQL 9.6.24 的新库、重复迁移与现有用户 fixture 升级冒烟已通过。
- RabbitMQ Compose：三个 Compose 已加入 `nova` profile；4.3.6 confirm、失败恢复、并发 claim、lease→dead 已通过。
- 测试和验证：阶段 6 强制矩阵已记录命令与版本；已知限制见 14.2。
- 下一个动作：由用户确认后提交（建议按第 14 节分阶段 commit，或一次合并不改受保护标识）；上线按第 11 节灰度顺序执行。不得复制旧原型分支实现。
