# New-API ↔ Nova 对接文档

本说明面向 **Nova 平台**（调用方 / 消息消费方）开发人员，描述 New-API 作为网关与账本方向 Nova 平台暴露的**接口**与**消息**契约：如何通过 HMAC 签名调用管理 API、如何在业务请求中声明用量归属、以及如何消费 RabbitMQ 上的用量消息。每个接口与消息均给出字段含义与枚举值。

| 项 | 值 |
|---|---|
| 管理 API 前缀 | `/api/novapay` |
| 消息 Exchange | `nova.events`（topic，durable） |
| 用量消息 Routing Key | `nova.usage.reported` |
| 消息 Content-Type / Mode | `application/json` / persistent |
| 幂等去重键 | AMQP `Message-Id`（= 账本 `id` 的十进制字符串） |
| 交付语义 | **at-least-once**（重复投递由消费端按去重键去重） |

### 额度与美元换算

系统内部扣费使用 `quota`；美元金额按部署配置 `QuotaPerUnit` 换算：

```text
美元金额 = quota / QuotaPerUnit
quota = 美元金额 × QuotaPerUnit
```

默认 `QuotaPerUnit = 500000`，即 `500000 quota = $1`、`1000 quota = $0.002`。账本和 RabbitMQ 消息的 `quota_data.deducted_amount_usd` 使用同一公式。

---

## 1. 概述

New-API 向 Nova 平台提供三部分能力：

1. **管理 API**（`/api/novapay/*`，全部要求 HMAC 签名）：租户创建 / 查询 / 列表 / 启停 / 软删除、额度的调整与查询、令牌（Token）的创建与轮换吊销、用量账本拉取、模型目录、健康检查。
2. **业务请求归属**：使用 Nova 租户分配的 Token 调用模型 / 任务时，通过请求头声明“该调用属于哪个租户”，使计费正确落到对应租户。
3. **用量消息**（RabbitMQ）：仅当业务请求**最终成功且完成结算**后，把一条用量明细异步推送到 `nova.events` / `nova.usage.reported`。

典型对接流程：

```text
Nova 平台                          New-API
   │                                   │
   │ 1. POST /api/novapay/tenant       │   创建租户 + 初始额度 + 初始 Token
   │    （HMAC 签名）                   │   → 返回 token_key（明文仅此返回）
   │<──────────────────────────────────│
   │                                   │
   │ 2. POST /v1/chat/completions      │   业务请求（Bearer token + Nova 归属头 + HMAC 签名）
   │    （调用模型）                     │   → 计费并落账
   │<──────────────────────────────────│
   │                                   │
   │ 3. RabbitMQ: nova.events          │   成功结算后按 nova.usage.reported 推送用量明细
   │    （Message-Id 去重）             │
   └───────────────────────────────────┘
   （亦可用 GET /api/novapay/tenant/{key}/logs 主动拉取同一账本）
```

### 1.1 对接方配置清单

对接启动前，New-API 部署方需向 Nova 平台交付以下**连接信息与凭据**；Nova 侧需自持 / 自建的参数见“说明”。**真实密钥、口令、Token 一律由部署方线下交付**，不得提交进仓库、业务日志或 MQ 消息。

| 配置项 | 格式 / 默认值 | 说明 |
|---|---|---|
| New-API 接口地址 | `http://<host>:<port>` | 管理 API 根地址，路径前缀 `/api/novapay` |
| 模型 / 任务调用地址 | `http://<host>:<port>` | 业务请求基址，走既有 `/v1/*` 与任务接口 |
| HMAC `Key Id` | 如 `current` | 随请求头 `X-Nova-Key-Id` 传递；省略时使用当前密钥（单密钥部署） |
| HMAC `Secret` | base64url 编码，解码后 ≥32 字节 | 用于计算 `X-Nova-Signature`；密钥轮换期至多同时接受 2 把 |
| 时间窗 / nonce 保留期 | 默认 ±300 秒 / 600 秒 | 超出时间窗或重放 nonce 即拒绝（服务端可调） |
| RabbitMQ AMQP 地址 | `amqp://<user>:<pass>@<host>:5672/<vhost>` | 用量消息消费侧的连接信息 |
| Exchange | `nova.events`（topic，durable） | 消费方自建队列绑定该 exchange |
| Routing Key | `nova.usage.reported` | 绑定关系与消息结构见第 6 节 |
| `tenant_key` | 3–20 位，`^[A-Za-z0-9][A-Za-z0-9._-]{2,19}$` | 由 Nova 侧调用「创建租户」获得；创建后不可变更 |
| 调用 Token | `sk-` 开头 | 创建租户响应中的 `token_key`；明文仅在创建 / 轮换 / 令牌列表接口返回 |
| 可用模型 | — | 以 `GET /api/novapay/models` 返回为准 |

**本机联调参考**（仅限本地开发环境，**生产配置另外提供**）：

| 项 | 值 |
|---|---|
| New-API 地址 | `http://127.0.0.1:3000` |
| RabbitMQ AMQP 地址 | `amqp://nova_dev:nova_dev_only@127.0.0.1:5672/` |
| HMAC Key Id / Secret | `current` / `I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk` |
| Exchange / Routing Key | `nova.events` / `nova.usage.reported` |
| 调用 Token | 通过「创建租户」接口创建后取得，不以本表预定 |

---

### 1.2 Postman 联调

仓库提供可直接导入 Postman 的 Collection 和 Environment，适合在接入、排障时逐个调用 Nova HTTP 接口：

- [Nova Integration Lab Collection](postman/Nova-Integration-Lab.postman_collection.json)：包含全部 15 个管理 API 和 1 个模型调用 API；在请求发送前自动生成 `X-Nova-Timestamp`、`X-Nova-Nonce`、请求标识，并使用 Web Crypto 计算 `X-Nova-Signature`。
- [Nova Integration Lab Environment](postman/Nova-Integration-Lab.postman_environment.json)：仅预置 `nova_base_url` 为本机地址，其余变量均为空，避免导入环境携带凭据或业务租户信息。

导入两个文件并选择该环境后，填写以下环境变量的**当前值**：

| 变量 | 必填场景 | 说明 |
|---|---|---|
| `nova_base_url` | 全部请求 | New-API 服务根地址；默认 `http://127.0.0.1:3000` |
| `nova_hmac_secret` | 全部请求 | 部署方交付的 base64url HMAC Secret，解码后至少 32 字节 |
| `nova_key_id` | 可选 | HMAC Key Id；单密钥部署可留空 |
| `tenant_key` | 租户及模型请求 | Nova 租户标识；创建租户前自行指定 |
| `token_name` | 令牌额度 / 吊销 | 对应的令牌名称 |
| `api_token` | 模型请求 | Nova 租户 Token；「创建租户」和「轮换主令牌」成功后会自动更新 |
| `model` | 模型请求 | 要调用的模型名称 |

Collection 不包含 RabbitMQ Management API 操作；RabbitMQ 消费端请按第 6 节使用 AMQP 客户端自行声明队列、绑定 Exchange 并消费消息。签名规则仍以第 2 节为准。

---

## 2. HMAC 认证

管理 API 的全部接口（含 `/health`）以及业务请求归属头共用同一套 HMAC-SHA256 签名算法。

### 2.1 请求头

| 请求头 | 必填 | 规则与含义 |
|---|---|---|
| `X-Nova-Key-Id` | 否 | 签名所用密钥的标识。省略时使用服务端当前密钥（单密钥部署无需传） |
| `X-Nova-Timestamp` | 是 | Unix 秒（UTC）。必须位于服务端允许的时间窗内，默认 ±300 秒（超出即拒绝） |
| `X-Nova-Nonce` | 是 | 随机串，长度 22–128 位，仅含 `[A-Za-z0-9_-]`；同一种子不可重复使用（防重放） |
| `X-Nova-Signature` | 是 | HMAC-SHA256 签名结果的小写 hex，共 64 字符 |

> 说明：服务端默认时间窗为 ±300 秒、nonce 保留期 600 秒（可按部署调整）。超窗的时间戳与已被使用过的 nonce 一律拒绝。

### 2.2 Canonical Request

签名原文（canonical string）按换行 `\n` 拼接，顺序固定：

```text
METHOD
ESCAPED_PATH
CANONICAL_QUERY
TIMESTAMP
NONCE
SHA256_HEX(BODY)
```

| 行 | 规则 |
|---|---|
| `METHOD` | HTTP 方法，大写，如 `POST` |
| `ESCAPED_PATH` | 请求 URL 的转义路径（`url.EscapedPath()`）；为空时用 `/` |
| `CANONICAL_QUERY` | query 解析后按 key 排序、同一 key 的多值再排序后重新 `url.Encode()` 生成；无 query 时为空字符串 |
| `TIMESTAMP` | 与请求头 `X-Nova-Timestamp` 原文一致 |
| `NONCE` | 与请求头 `X-Nova-Nonce` 原文一致 |
| `SHA256_HEX(BODY)` | 请求体原始字节的 SHA-256 小写 hex；无请求体时对空字节数组哈希（即 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`） |

### 2.3 签名

```text
signature = hex( HMAC-SHA256( secret_bytes, canonical_string ) )
```

- `secret_bytes`：服务端下发的密钥，按 base64url 解码（无填充）。
- 签名必须以 hex 小写形式放入 `X-Nova-Signature`。

### 2.4 密钥与轮换

- 服务端最多同时接受 2 把密钥；新请求以 **current**（第一把，通常以 `X-Nova-Key-Id` 显式指定）签名。
- 轮换流程：先新增一把作为 previous 并由服务端切换到新 current，旧 key 仍被接受（供灰度窗口内请求使用），确认旧请求全部下线后再移除上一把。

### 2.5 失败响应

鉴权失败统一返回 `401`，不区分具体失败原因，响应体：

```json
{
  "success": false,
  "code": "nova_authentication_failed",
  "message": "service authentication failed",
  "request_id": "xxxxxx"
}
```

---

## 3. 通用响应格式与错误码

所有管理 API 都使用统一包裹结构。

**成功：**

```json
{
  "success": true,
  "message": "ok",
  "data": { "…": "…" }
}
```

**失败：**

```json
{
  "success": false,
  "code": "……",
  "message": "……",
  "request_id": "……"
}
```

| 字段 | 类型 | 含义 |
|---|---|---|
| `success` | bool | 是否成功 |
| `message` | string | 简要说明；成功固定为 `ok` |
| `data` | object | 成功时的业务数据 |
| `code` | string | 失败错误码（枚举见下表） |
| `request_id` | string | New-API 侧请求号，便于平台侧与 New-API 运维联查 |

### 错误码表（枚举）

| HTTP | `code` | 含义与触发场景 |
|---|---|---|
| 400 | `invalid_request` | 请求体解析失败、字段格式/取值校验失败、`request_id` / `order_no` 非法、调额时 `delta_quota` 与 `absolute_quota` 同时传或都不传 |
| 400 | `invalid_quota` | 额度超出允许范围（`0 .. 2^53-1`） |
| 400 | `invalid_cursor` | `last_id` 非法（非数字或小于 0） |
| 400 | `invalid_size` | `size` 非法（越界 1–100） |
| 400 | `invalid_time_range` | `start_time` / `end_time` 非法或 `start_time > end_time` |
| 400 | `invalid_pagination` | 分页参数非法（`page < 1` 或 `page_size` 越界 1–100） |
| 401 | `nova_authentication_failed` | HMAC 签名 / 时间窗 / nonce / 密钥任一项校验失败 |
| 403 | `nova_tenant_unavailable` | 业务请求所归属租户被禁用或已删除 |
| 404 | `tenant_not_found` | 租户不存在（或 `tenant_key` 格式非法） |
| 404 | `token_not_found` | 指定令牌不存在 |
| 409 | `tenant_exists` | 创建租户时 `tenant_key` 已存在 |
| 409 | `tenant_deleted` | 对已软删除租户执行修改 / 启用等写操作 |
| 409 | `idempotency_conflict` | 同一 `request_id` 但请求体与上次不同 |
| 409 | `operation_conflict` | 同一 `order_no` 但调额参数与上次不同 |
| 409 | `operation_in_progress` | 同一 `request_id` 的并发请求仍在处理中 |
| 409 | `quota_out_of_range` | 调额将导致余额越界（减到负值或超过上限） |
| 500 | `internal_error` | 服务端内部错误 |

---

## 4. 管理 API

### 4.1 公共字段约束

| 字段 | 约束 / 格式 | 含义 |
|---|---|---|
| `tenant_key` | `^[A-Za-z0-9][A-Za-z0-9._-]{2,19}$`（3–20 位，首字符须为字母/数字） | 租户唯一标识，即 New-API 用户名；创建后**不可变更** |
| `tenant_name` / `display_name` | 1–20 字符 | 租户显示名称 |
| `token_name` | `^[A-Za-z0-9][A-Za-z0-9._-]{0,49}$`（1–50 位） | 令牌名称 |
| `request_id` | `^[A-Za-z0-9._:-]{8,128}$` | 幂等键；**所有写接口必传**，建议每次使用唯一值（如 UUID） |
| `order_no` | 1–128 字符，`[A-Za-z0-9._:-]` | 调额单号（业务防重键），仅调额接口使用 |
| `quota` | 非负整数，`0 .. 2^53-1` | 配额/额度单位 |
| 分页 | `page` ≥ 1（默认 1）；`page_size` 1–100（默认 20） | 列表接口通用分页 |

### 4.2 幂等规则

- **请求级幂等（全部写接口）**：以 `request_id` 幂等。相同 `request_id` + 相同请求体重放 → 直接返回首次结果，且响应头带 `Idempotency-Replayed: true`；相同 `request_id` + 不同请求体 → `409 idempotency_conflict`。网络超时后以同一 `request_id` 重试即可安全去重。
- **调额业务级防重（`order_no`）**：租户调额额外以 `order_no` 防重。相同 `order_no` + 相同参数重放 → 不重复加减额度，响应体 `data.replayed=true`（此时不带 `Idempotency-Replayed` 响应头）；相同 `order_no` + 不同参数 → `409 operation_conflict`。新调额必须使用新 `order_no`。

### 4.3 路由一览

| 方法 | 路径 | 需 `request_id` | 用途 |
|---|---|---|---|
| `GET` | `/api/novapay/health` | 否 | 健康检查 |
| `POST` | `/api/novapay/tenant` | 是 | 创建租户 + 专属用户 + 初始 Token |
| `GET` | `/api/novapay/tenant/{tenant_key}` | 否 | 查询租户 |
| `PUT` | `/api/novapay/tenant/{tenant_key}` | 是 | 更新租户显示名 |
| `DELETE` | `/api/novapay/tenant/{tenant_key}` | 是 | 软删除租户 |
| `GET` | `/api/novapay/tenants` | 否 | 分页查询租户列表 |
| `POST` | `/api/novapay/tenant/{tenant_key}/quota` | 是 | 调整租户额度（增量 / 设定绝对值） |
| `POST` | `/api/novapay/tenant/{tenant_key}/disable` | 是 | 禁用租户及其全部 Token |
| `POST` | `/api/novapay/tenant/{tenant_key}/enable` | 是 | 启用租户 |
| `POST` | `/api/novapay/tenant/{tenant_key}/token/rotate` | 是 | 轮换主令牌密钥 |
| `GET` | `/api/novapay/tenant/{tenant_key}/tokens` | 否 | 查询租户全部令牌（含明文 key） |
| `POST` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}/quota` | 是 | 设置令牌额度 |
| `DELETE` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}` | 是 | 吊销令牌 |
| `GET` | `/api/novapay/tenant/{tenant_key}/logs` | 否 | 拉取用量账本 |
| `GET` | `/api/novapay/models` | 否 | 模型目录 |

> 提示：管理面有接口级频率限制（按客户端 IP），超限会返回 `429`，需等待限流窗口过后再重试，联调时避免短时间高频调用写接口。

### 4.4 创建租户 `POST /api/novapay/tenant`

**请求体：**

| 字段 | 类型 | 必填 | 含义 / 枚举 |
|---|---|---|---|
| `tenant_key` | string | 是 | 租户唯一标识（格式见 4.1） |
| `tenant_name` | string | 是 | 租户显示名称，1–20 字符 |
| `initial_quota` | int | 是 | 初始额度：同时写入用户余额与初始 Token 的额度 |
| `request_id` | string | 是 | 幂等键 |

**请求示例：**

```json
{
  "tenant_key": "nova-tenant-001",
  "tenant_name": "示例科技有限公司",
  "initial_quota": 5000000,
  "request_id": "req-20260921-0001"
}
```

**响应 `data` 字段：**

| 字段 | 类型 | 含义 |
|---|---|---|
| `user_id` | int | New-API 内部用户 id |
| `username` | string | 用户名（= `tenant_key`） |
| `token_name` | string | 初始令牌名称，默认 `nova-{tenant_key}`（超过 50 字符截断） |
| `token_key` | string | **完整明文密钥**，形如 `sk-……`；仅创建 / 轮换 / 令牌列表接口返回明文 |
| `quota` | int | 当前用户余额 |

**响应示例：**

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "user_id": 101,
    "username": "nova-tenant-001",
    "token_name": "nova-nova-tenant-001",
    "token_key": "sk-xxxxxxxxxxxxxxxxxxxxxxxx",
    "quota": 5000000
  }
}
```

**异常：** `409 tenant_exists`（重名）；`400 invalid_request` / `invalid_quota`。

### 4.5 查询租户 `GET /api/novapay/tenant/{tenant_key}`

**响应 `data` 字段：**

| 字段 | 类型 | 含义 / 枚举 |
|---|---|---|
| `user_id` | int | 用户 id |
| `username` | string | 用户名（= `tenant_key`） |
| `display_name` | string | 显示名称 |
| `status` | string | 枚举：`enabled` 启用 / `disabled` 禁用 / `deleted` 已软删除 |
| `quota` | int | 用户余额 |
| `used_quota` | int | 已用额度 |
| `token_name` | string | 当前启用令牌名（无启用令牌时取最近令牌） |
| `token_key` | string | **掩码**密钥（如 `sk-DXql**********MfmB`）；明文仅创建/轮换/列表接口返回 |
| `created_at` | string | 创建时间，RFC3339（UTC） |
| `last_active_at` | string | 最近活跃时间，RFC3339（UTC）；取“最近成功用量”与“最近登录”中较晚者，无则回退为创建时间 |

**响应示例：**

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "user_id": 101,
    "username": "nova-tenant-001",
    "display_name": "示例科技有限公司",
    "status": "enabled",
    "quota": 4896554,
    "used_quota": 103446,
    "token_name": "nova-nova-tenant-001",
    "token_key": "sk-DXql**********MfmB",
    "created_at": "2026-09-21T00:00:00Z",
    "last_active_at": "2026-09-21T07:50:53Z"
  }
}
```

> 说明：已软删除的租户仍返回 `200`，以 `data.status=deleted` 判断。返回 `404 tenant_not_found` 当租户不存在。

### 4.6 更新租户 `PUT /api/novapay/tenant/{tenant_key}`

**请求体：** `request_id`（必填）、`display_name`（必填，1–20 字符）。

**响应 `data`：** 同 4.5 的租户详情结构。

**请求示例：**

```json
{ "request_id": "req-20260921-0002", "display_name": "新名称" }
```

### 4.7 软删除租户 `DELETE /api/novapay/tenant/{tenant_key}`

**请求体：** `{"request_id":"……"}`（必填）。

软删除语义：用户与全部令牌置为 `disabled` 并标记删除；**历史账本与用量保留**。响应 `data`：`{"username": "{tenant_key}", "status": "deleted"}`。

> 对已软删除租户执行任何写操作（含再次 `enable`）→ `409 tenant_deleted`。

### 4.8 租户列表 `GET /api/novapay/tenants`

**Query 参数：**

| 参数 | 类型 | 必填 | 含义 / 枚举 |
|---|---|---|---|
| `page` | int | 否 | 页码，默认 1 |
| `page_size` | int | 否 | 每页条数，1–100，默认 20 |
| `keyword` | string | 否 | 按 `username` / `display_name` 模糊匹配（不区分大小写） |
| `status` | string | 否 | 枚举：`enabled` / `disabled` / `deleted` |

**响应 `data` 字段：**

| 字段 | 类型 | 含义 |
|---|---|---|
| `total` | int | 满足条件总数 |
| `page` | int | 当前页 |
| `page_size` | int | 每页条数 |
| `items` | array | 租户列表 |

**`items[]` 单项字段：**

| 字段 | 类型 | 含义 / 枚举 |
|---|---|---|
| `username` | string | 用户名（= `tenant_key`） |
| `user_id` | int | 用户 id |
| `display_name` | string | 显示名称 |
| `status` | string | 枚举：`enabled` / `disabled` / `deleted` |
| `quota` | int | 余额 |
| `used_quota` | int | 已用额度 |
| `created_at` | string | 创建时间，RFC3339（UTC） |
| `last_active_at` | string | 最近活跃时间，RFC3339（UTC） |

> 列表接口与详情接口的时间字段均为 **RFC3339 字符串**。列表不返回任何 token 明文。

**响应示例：**

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "total": 1,
    "page": 1,
    "page_size": 20,
    "items": [
      {
        "username": "nova-tenant-001",
        "user_id": 101,
        "display_name": "示例科技有限公司",
        "status": "enabled",
        "quota": 4896554,
        "used_quota": 103446,
        "created_at": "2026-09-21T00:00:00Z",
        "last_active_at": "2026-09-21T07:50:53Z"
      }
    ]
  }
}
```

### 4.9 调整租户额度 `POST /api/novapay/tenant/{tenant_key}/quota`

**请求体：**

| 字段 | 类型 | 必填 | 含义 |
|---|---|---|---|
| `request_id` | string | 是 | 幂等键 |
| `order_no` | string | 是 | 调额单号（业务防重键）；一个单号只生效一次 |
| `delta_quota` | int | 二选一 | 增量（可为负） |
| `absolute_quota` | int | 二选一 | 设定目标绝对值 |
| `reason` | string | 是 | 事由，1–255 字符 |

> `delta_quota` 与 `absolute_quota` **必须且只能传一个**。调整的是**用户余额**（`users.quota`），不是某一令牌的额度（令牌额度用 4.13）。

**请求示例（增量）：**

```json
{
  "request_id": "req-20260921-0003",
  "order_no": "R2026092100001",
  "delta_quota": 1000000,
  "reason": "recharge"
}
```

**响应 `data` 字段：**

| 字段 | 类型 | 含义 |
|---|---|---|
| `username` | string | 用户名 |
| `quota_before` | int | 调整前余额 |
| `quota_after` | int | 调整后余额 |
| `delta_quota` | int | 实际增量（= `quota_after - quota_before`） |
| `order_no` | string | 原样返回单号 |
| `reason` | string | 原样返回事由 |
| `replayed` | bool | 是否命中 `order_no` 防重返回（重复单号不重复加减） |

**响应示例：**

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "username": "nova-tenant-001",
    "quota_before": 4896554,
    "quota_after": 5896554,
    "delta_quota": 1000000,
    "order_no": "R2026092100001",
    "reason": "recharge",
    "replayed": false
  }
}
```

**异常：** `409 operation_conflict`（同 `order_no` 但参数不同）；`409 quota_out_of_range`（减到负余额或超过上限 `2^53-1`）。

### 4.10 禁用 / 启用租户

`POST /api/novapay/tenant/{tenant_key}/disable` 与 `POST /api/novapay/tenant/{tenant_key}/enable`，请求体均为 `{"request_id":"……"}`。

- **禁用**：租户用户与全部令牌置为 `disabled`。
- **启用**：仅将租户用户恢复为 `enabled`，**不会**恢复曾被单独禁用的令牌。

响应 `data`：`{"username": "{tenant_key}", "status": "disabled" | "enabled"}`。

> 对已软删除租户执行 `enable` → `409 tenant_deleted`。

### 4.11 轮换主令牌 `POST /api/novapay/tenant/{tenant_key}/token/rotate`

**请求体：** `request_id`（必填）、`reason`（必填，1–255 字符）。

**响应 `data` 字段：**

| 字段 | 类型 | 含义 |
|---|---|---|
| `old_token_name` | string | 被轮换令牌原名称 |
| `token_name` | string | 令牌名称（不变） |
| `token_key` | string | **新密钥明文**（`sk-……`）；旧密钥立即失效 |

> 轮换不影响令牌名称与额度；新明文仅在首次请求及相同 `request_id` 重放时返回。

### 4.12 令牌列表 `GET /api/novapay/tenant/{tenant_key}/tokens`

**响应 `data.items[]` 字段：**

| 字段 | 类型 | 含义 / 枚举 |
|---|---|---|
| `token_id` | int | 令牌 id |
| `token_name` | string | 令牌名称 |
| `key` | string | **完整明文密钥**（`sk-……`；仅管理面 HMAC 请求可见） |
| `status` | string | 枚举：`enabled` 启用 / `disabled` 禁用 / `expired` 过期 / `exhausted` 额度耗尽 |
| `expired_time` | int | 过期时间，Unix 秒；`-1` 表示永不过期 |
| `remain_quota` | int | 剩余额度 |
| `unlimited_quota` | bool | 是否不限额度 |
| `used_quota` | int | 已用额度 |
| `created_time` | int | 创建时间，Unix 秒 |

### 4.13 设置令牌额度 `POST /api/novapay/tenant/{tenant_key}/tokens/{token_name}/quota`

**请求体：**

| 字段 | 类型 | 必填 | 含义 |
|---|---|---|---|
| `request_id` | string | 是 | 幂等键（仅依赖 `request_id`，无 `order_no`） |
| `remain_quota` | int | 是 | 设置后的剩余额度 |
| `unlimited_quota` | bool | 是 | 是否不限额度 |
| `reason` | string | 是 | 事由，1–255 字符 |

**响应 `data`：** `{"token_name": "……", "remain_quota": 500000, "unlimited_quota": false}`。

> 若令牌此前因额度耗尽为 `exhausted`，设置后额度可用（`remain_quota > 0` 或 `unlimited_quota=true`）时自动恢复为 `enabled`。

### 4.14 吊销令牌 `DELETE /api/novapay/tenant/{tenant_key}/tokens/{token_name}`

**请求体：** `{"request_id":"……"}`。将指定令牌置为 `disabled`。

**响应 `data`：** `{"username": "{tenant_key}", "token_name": "{token_name}", "status": "disabled"}`。

### 4.15 用量账本 `GET /api/novapay/tenant/{tenant_key}/logs`

按账本行 `id` 升序分页拉取。**无** `total` / `page` / `page_size`，使用游标 `last_id` 续拉。

**Query 参数：**

| 参数 | 类型 | 必填 | 含义 |
|---|---|---|---|
| `last_id` | int | 否 | 游标：仅返回 `id` 大于该值的记录；省略或 `0` 从最早开始 |
| `size` | int | 否 | 每页条数 1–100，默认 100 |
| `start_time` | int | 否 | 起始时间（Unix 秒，含）；按账本 `occurred_at` 过滤 |
| `end_time` | int | 否 | 结束时间（Unix 秒，含） |

**响应 `data` 字段：**

| 字段 | 类型 | 含义 |
|---|---|---|
| `has_more` | bool | 本页条数是否等于 `size`；为 `true` 时用本页最后一条的 `id` 作为下次 `last_id` |
| `items` | array | 用量明细（单项结构见第 6 节 RabbitMQ 消息，二者一致） |

> **续拉必须用账本行 `id`，不要用 `log.id`**：消费日志先落库、账本行后补，按 `log.id` 续拉会漏。对账键仍用 `log.id`。`has_more=true` 仅表示本页写满，即使后面已无数据，继续请求会得到空列表。

### 4.16 模型目录 `GET /api/novapay/models`

返回系统模型定价目录。`enabled` 表示当前是否有启用渠道，`channel_count` 为启用该模型的渠道数量（`enabled = channel_count > 0`），平台侧据此筛选可实际路由的模型。**不返回**渠道密钥、上游地址、渠道名等内部信息。

**响应 `data.items[]` 字段：**

| 字段 | 类型 | 含义 / 枚举 |
|---|---|---|
| `model_name` | string | 模型名 |
| `channel_count` | int | 启用该模型的渠道数量 |
| `enabled` | bool | 是否可用（`channel_count > 0`） |
| `vendor_name` | string | 上游厂商（配置了才返回） |
| `description` / `tags` | string | 模型描述 / 标签（配置了才返回） |
| `quota_type` | int | **枚举：`0` 按 token 倍率计费；`1` 按次计价** |
| `model_ratio` | float | （`quota_type=0` 时）token 倍率 |
| `completion_ratio` | float | （`quota_type=0` 时）输出与输入倍率比 |
| `model_price` | float | （`quota_type=1` 时）单次价格 |
| `cache_ratio` / `create_cache_ratio` / `image_ratio` / `audio_ratio` / `audio_completion_ratio` | float | 各类计费倍率（配置了才返回） |
| `enable_groups` | array | 允许使用的分组列表 |
| `supported_endpoint_types` | array | 支持的接口类型，枚举值见下 |

**`supported_endpoint_types` 枚举（`relaykit/types/endpoint_type.go`）：**

| 值 | 含义 |
|---|---|
| `openai` | OpenAI 兼容 chat 接口（`/v1/chat/completions` 等） |
| `openai-response` | OpenAI Responses 接口 |
| `openai-response-compact` | Responses 紧凑格式接口 |
| `anthropic` | Anthropic 接口 |
| `gemini` | Gemini 接口 |
| `jina-rerank` | Jina 重排接口 |
| `image-generation` | 图像生成接口 |
| `embeddings` | 向量接口 |

**响应示例：**

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "items": [
      {
        "model_name": "deepseek-r1",
        "channel_count": 1,
        "enabled": true,
        "vendor_name": "DeepSeek",
        "quota_type": 0,
        "model_ratio": 37.5,
        "completion_ratio": 1,
        "enable_groups": ["default"],
        "supported_endpoint_types": ["openai"]
      },
      {
        "model_name": "qwen-image-plus",
        "channel_count": 1,
        "enabled": true,
        "quota_type": 1,
        "model_price": 0.028671,
        "enable_groups": ["default"],
        "supported_endpoint_types": ["image-generation", "openai"]
      }
    ]
  }
}
```

### 4.17 健康检查 `GET /api/novapay/health`

**响应 `data` 字段：**

| 字段 | 类型 | 含义 / 枚举 |
|---|---|---|
| `status` | string | 枚举：`up` 正常 / `degraded` 降级（主库可用但 MQ 或 Redis 异常）/ `down` 不可用（主库不可用） |
| `database` | object | `{"connected": bool}`，主库连接是否正常 |
| `redis` | object | `{"enabled": bool, "connected": bool}`；未启用 Redis 时 `enabled=false` 且不参与降级判定 |
| `mq` | object | `{"connected": bool, "outbox_pending": int, "outbox_dead": int}`；`outbox_pending` / `outbox_dead` 为待投递 / 失败的消息计数 |

**响应示例：**

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "status": "up",
    "database": { "connected": true },
    "redis": { "enabled": true, "connected": true },
    "mq": { "connected": true, "outbox_pending": 0, "outbox_dead": 0 }
  }
}
```

> 建议监控：`database.connected == false`、`mq.connected == false`、`mq.outbox_dead > 0` 立即告警；`mq.outbox_pending` 持续增长说明消息推送积压。

---

## 5. 业务请求归属

使用某 Nova 租户分配的 Token 调用模型 / 任务时，除 `Authorization: Bearer {token}` 外，还需在请求头中声明归属并签名。**非 Nova 租户的普通 Token 行为不变**，无需这些头。

### 5.1 归属头

| 请求头 | 必填 | 含义 / 规则 |
|---|---|---|
| `X-Nova-Tenant-Key` | 是 | 该 Token 所属租户的 `tenant_key`，必须与 Token 实际归属一致 |
| `X-Nova-Request-Id` | 否 | 平台侧请求标识，随用量消息返回（落在 `nova_request_id`），建议携带以便对账 |
| `X-Nova-Timestamp` / `X-Nova-Nonce` / `X-Nova-Signature` | 是 | 对**本次业务请求**的 METHOD / PATH / QUERY / BODY 签名（算法与第 2 节相同） |

**请求示例：**

```http
POST /v1/chat/completions HTTP/1.1
Host: <new-api-host>
Authorization: Bearer sk-xxxxxxxxxxxxxxxxxxxxxxxx
Content-Type: application/json
X-Nova-Tenant-Key: nova-tenant-001
X-Nova-Request-Id: nova-req-<uuid>
X-Nova-Timestamp: <unix秒>
X-Nova-Nonce: <随机串>
X-Nova-Signature: <HMAC-SHA256 hex>

{
  "model": "deepseek-r1",
  "messages": [{"role": "user", "content": "只回复 ok"}],
  "max_tokens": 8,
  "stream": false
}
```

### 5.2 校验规则

1. Token 属于 Nova 租户时，该租户必须存在且为 `enabled`；否则返回 `403 nova_tenant_unavailable`。
2. HMAC 签名必须有效，且 `X-Nova-Tenant-Key` 必须等于该 Token 的租户；任一项不满足返回 `401 nova_authentication_failed`。

### 5.3 用量上报口径（枚举）

| 场景 | 是否上报 | `source` |
|---|---|---|
| 同步请求最终结算成功 | 是 | `relay`（New-API 内部请求号） |
| 异步任务最终成功且结算完成 | 是 | `task`（`task_id`） |
| 预扣、失败、退款、进行中 | 否 | — |

- 仅上报**最终成功且已结算**的用量。
- `quota == 0` 的成功请求默认仍会发布（便于审计）。

---

## 6. RabbitMQ 用量消息

### 6.1 投递参数

| 项 | 值 |
|---|---|
| Exchange | `nova.events`（topic，durable） |
| Routing Key | `nova.usage.reported` |
| Delivery Mode | persistent |
| Content-Type | `application/json` |
| Message-Id | 等于账本 `id` 的十进制字符串（**幂等去重键**；仅作为 AMQP 属性传输） |
| 消费队列 | 由消费方自行声明并绑定上述 exchange + routing key；服务端**不代建队列** |

### 6.2 消息结构

消息 payload 与 `GET /api/novapay/tenant/{key}/logs` 的 `data.items[]` 单项**结构完全一致**（不含 `has_more`）。
消费者必须从 AMQP `Message-Id` 读取幂等去重键；其值与 payload 的数值字段 `id` 表示同一账本行。

**顶层字段：**

| 字段 | 类型 | 含义 / 枚举 |
|---|---|---|
| `id` | int | 账本行主键，用于排序与 `last_id` 续拉 / 对账 |
| `tenant_key` | string | 归属租户 |
| `user_id` | int | 用户 id |
| `request_id` | string | New-API 内部请求号 |
| `nova_request_id` | string | 平台侧业务请求携带的 `X-Nova-Request-Id`；未携带时为空字符串 |
| `model_type` | string | **枚举：`text` 文本 / `image` 图像 / `video` 视频**（按请求路径推断） |
| `log` | object | 消费日志字段（见 6.3） |
| `quota_data` | object | 计费字段（见 6.4） |
| `other` | object | 其余非敏感扩展字段（见 6.5），可能为空对象或省略 |

**示例：**

```json
{
  "id": 15,
  "tenant_key": "nova-tenant-001",
  "user_id": 101,
  "request_id": "20260921073718921707",
  "nova_request_id": "nova-req-550e8400-e29b-41d4-a716-446655440000",
  "model_type": "video",
  "log": {
    "id": 111,
    "type": 2,
    "model_name": "wanx2.1-t2v-turbo",
    "prompt_tokens": 0,
    "completion_tokens": 0,
    "total_tokens": 0,
    "created_at": "2026-09-21T07:37:19Z",
    "is_stream": false,
    "use_time": 31.2,
    "token_id": 7,
    "token_name": "nova-nova-tenant-001",
    "channel_id": 1,
    "channel_name": "wanx",
    "group": "default"
  },
  "quota_data": {
    "quota": 86012,
    "created_time": "2026-09-21T07:37:19Z",
    "deducted_amount_usd": 0.172024,
    "billing_mode": "tiered_expr",
    "matched_tier": "video",
    "expr": "tier(\"video\", u(\"seconds\") * 0.034405)",
    "usage_facts": { "resolution": "480P", "seconds": 5 }
  },
  "other": {
    "request_path": "/v1/videos",
    "task_id": "task_xxxxxxxx",
    "is_task": true
  }
}
```

### 6.3 `log` 对象字段

| 字段 | 类型 | 含义 / 枚举 |
|---|---|---|
| `id` | int | 消费日志 id，**唯一对账键**（跨消息 / 跨接口去重均以它对齐） |
| `type` | int | 日志类型，Nova 消息固定为 **`2` 消费（consume）**；完整枚举：`0` 未知 / `1` 充值 / `2` 消费 / `3` 管理 / `4` 系统 / `5` 错误 / `6` 退款 / `7` 登录 |
| `model_name` | string | 实际计费模型名 |
| `prompt_tokens` | int | 输入 token 数 |
| `completion_tokens` | int | 输出 token 数 |
| `total_tokens` | int | 总 token 数（`prompt + completion`） |
| `created_at` | string | 请求完成时间，RFC3339（UTC） |
| `is_stream` | bool | 是否流式请求 |
| `use_time` | number | 请求耗时（秒，可能为小数） |
| `token_id` | int | 使用令牌 id（存在才返回） |
| `token_name` | string | 使用令牌名称（存在才返回） |
| `channel_id` | int | 渠道 id（存在才返回） |
| `channel_name` | string | 渠道名称（存在才返回） |
| `group` | string | 所属分组（存在才返回） |
| `upstream_request_id` | string | 上游请求号（存在才返回） |
| `ip` | string | 客户端 IP（存在才返回） |

> 另有一组来自消费日志 `other` 的令牌/时序扩展字段也会出现在 `log` 中（存在才返回）：`frt`（首响应耗时，毫秒）、`cache_tokens`、`cache_creation_tokens`、`cache_creation_tokens_5m`、`cache_creation_tokens_1h`、`cache_write_tokens`、`reasoning_tokens`、`input_tokens_total`、`image_count`、`image_output`、`image_cache_tokens`、`audio_input`、`audio_output`、`text_input`、`text_output`、`audio_input_token_count`。

### 6.4 `quota_data` 对象字段

| 字段 | 类型 | 含义 / 枚举 |
|---|---|---|
| `quota` | int | 本次扣费配额数 |
| `created_time` | string | 与 `log.created_at` 同一时刻，RFC3339（UTC） |
| `deducted_amount_usd` | number | 折算美元 = `quota / QuotaPerUnit`；`QuotaPerUnit` 为部署配置，默认 `500000`（对应 $0.002/1K tokens） |
| `expr` | string | 本次计费所依据的计费表达式（服务端以 base64 存储，此处为解码后原文），便于对账（存在才返回） |
| `billing_mode` | string | 计费模式；当前取值为 **`tiered_expr`**（表达式 / 分档计费） |
| `billing_source` | string | 扣费来源，**枚举：`wallet` 钱包 / `subscription` 订阅** |
| `billing_preference` | string | 用户计费偏好（存在才返回） |
| `billing_unit` | string | 计费表达式的计费单位（存在才返回，如 `request`、`tokens`、`seconds` 等） |
| `billing_tokens` | object | 计费使用的 token 明细（存在才返回） |
| `matched_tier` | string | 命中的分档（存在才返回，如视频不同分辨率档位） |
| `usage_facts` | object | 计费事实快照（`string → string | number`），随模型与表达式不同而变化，如视频 `{"resolution":"480P","seconds":5}`（存在才返回） |
| `model_price` | float | 按次计价单价（`quota_type=1` 模型，存在才返回） |
| `model_ratio` / `completion_ratio` | float | 倍率计费时的输入 / 输出倍率（存在才返回） |
| `cache_ratio` / `cache_creation_ratio` / `cache_creation_ratio_5m` / `cache_creation_ratio_1h` | float | 缓存相关倍率（存在才返回） |
| `group_ratio` / `user_group_ratio` | float | 分组 / 用户分组倍率（存在才返回） |
| `fixed_price` | number | 固定计价（存在才返回） |
| `image_ratio` / `image_generation_call` / `image_generation_call_price` / `image_generation_call_count` | number | 图像生成计费分项（存在才返回） |
| `audio_ratio` / `audio_completion_ratio` / `audio_input_seperate_price` / `audio_input_price` | number | 音频计费分项（存在才返回） |
| `web_search` / `web_search_call_count` / `web_search_price` | number | 联网搜索计费分项（存在才返回） |
| `file_search` / `file_search_call_count` / `file_search_price` | number | 文件搜索计费分项（存在才返回） |
| `tool_surcharges` | array | 工具调用加价明细（存在才返回） |
| `request_rules` | array | 命中的请求规则（存在才返回） |
| `subscription_plan_id` / `subscription_plan_title` / `subscription_id` | string | 订阅相关标识（订阅计费时返回） |
| `subscription_pre_consumed` / `subscription_post_delta` / `subscription_consumed` / `subscription_remain` / `subscription_total` / `subscription_used` | number | 订阅扣减各分项（订阅计费时返回） |
| `wallet_quota_deducted` | number | 钱包实际扣减配额（订阅 + 钱包混合计费时返回） |
| `violation_fee` / `violation_fee_code` / `violation_fee_marker` | number / string | 违规扣费信息（存在才返回） |
| `fee_quota` | number | 额外费用配额（存在才返回） |

> 上述计费扩展字段均为“**存在才返回**”，不是每条消息都有；其中含义为比例/价格的字段均指 New-API 计费中的配置值。

### 6.5 `other` 对象字段

`other` 存放其余非敏感扩展字段，常见如：

| 字段 | 类型 | 含义 |
|---|---|---|
| `request_path` | string | 请求路径（用于 `model_type` 推断） |
| `task_id` | string | 异步任务 id（任务类用量） |
| `is_task` | bool | 是否任务类用量 |
| `content` | string | 摘要内容（存在才返回） |

**明确不出现**在消息中的字段：Token 明文、HMAC 密钥、`Authorization`、`admin_info` / `root_info` / `audit_info` 等角色级 / 内部字段。

### 6.6 消费要求（强制）

1. 以 AMQP `Message-Id`（即 payload `id` 的十进制字符串）做**幂等去重**（数据库唯一约束或等价机制）；重复投递应 ACK 且不重复记账。
2. 可用 `(request_id, nova_request_id, tenant_key)` 作业务侧辅助核对，**权威去重键仍是 `Message-Id`**。
3. 处理成功后再 ACK；失败 NACK / 重回队列，避免静默丢账。
4. 因交付为 **at-least-once**，同一消息可能在服务端重连 / 重试后重复投递，消费端必须容忍重复。

---

## 7. 术语对照

| 术语 | 含义 |
|---|---|
| `tenant_key` | 租户唯一标识（New-API 用户名），创建后不可变更 |
| Token / 令牌 | 调用模型所用的 `sk-` API 密钥；属于某一租户 |
| 额度（quota） | 计费用量单位；租户级余额存于用户，令牌级额度存于各令牌 |
| 账本 / 用量账本 | New-API 侧按成功结算记录写入的用量明细（`/logs` 与 MQ 消息同源） |
| `event_id` | 内部用量事件标识，等于账本 `id` 的十进制字符串，并作为 AMQP `Message-Id` 传输 |
| 归属 | 把一次业务请求计入特定租户的过程 |
