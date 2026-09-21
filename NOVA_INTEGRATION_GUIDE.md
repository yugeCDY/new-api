# New-API ↔ Nova 对接文档

面向 Nova 平台开发者的对接说明，涵盖管理 API、HMAC 认证、业务请求归属、RabbitMQ 用量消息与健康检查。

| 项 | 值 |
|---|---|
| 对应实现 | `integration/nova` |
| 管理 API 前缀 | `/api/novapay` |
| MQ Exchange | `nova.events`（topic，durable） |
| 用量 Routing Key | `nova.usage.reported` |
| 消息 Schema | 与 `/logs` 的 `data.items[]` 单项一致 |
| 交付语义 | **at-least-once**（消费端必须按 AMQP `Message-Id`（即 `event_id`）去重） |

---

## 1. 本机联调参数（当前生效）

> 以下为**当前运行中**的 New-API + RabbitMQ 实参，可直接联调。仅限本地测试，勿用于生产。若服务重启导致密钥/Token 变更，以运行中环境变量与数据库 `tokens` 表为准。

| 项 | 值 |
|---|---|
| New-API Base URL | `http://127.0.0.1:3000` |
| RabbitMQ AMQP | `amqp://nova_dev:nova_dev_only@127.0.0.1:5672/` |
| RabbitMQ 管理台 | `http://127.0.0.1:15672`（`nova_dev` / `nova_dev_only`） |
| HMAC Key Id | `current` |
| HMAC Secret（base64url，去 padding） | `I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk`（解码后 32 字节） |
| `tenant_key` | `nova-test-3` |
| Bearer Token | `sk-DXql71klnsJwizWBL6ZXp2oykxYoilgKLkfPOdPPmeoiMfmB` |
| 可用模型（本机渠道） | `deepseek-r1`（文本）、`qwen-image-plus`（图片）、`wanx2.1-t2v-turbo`（视频） |

环境变量等价写法：

```bash
export NOVA_BASE_URL=http://127.0.0.1:3000
export NOVA_KEY_ID=current
export NOVA_HMAC_SECRET=I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk
export NOVA_TENANT_KEY=nova-test-3
export NOVA_TOKEN=sk-DXql71klnsJwizWBL6ZXp2oykxYoilgKLkfPOdPPmeoiMfmB
export NOVA_TEST_MODEL=deepseek-r1
export RABBITMQ_URL=amqp://nova_dev:nova_dev_only@127.0.0.1:5672/
export RABBITMQ_MGMT=http://127.0.0.1:15672
export RABBITMQ_USER=nova_dev
export RABBITMQ_PASSWORD=nova_dev_only
```

**最小验证顺序**：

1. `GET /api/novapay/health` + 正确签名 → `200`，`data.status=up`。
2. `POST /v1/chat/completions`，模型 `deepseek-r1`，Bearer + HMAC + 归属头 → `200`。
3. 请求后 `GET /api/novapay/tenant/nova-test-3/logs` 出现新记录，自建队列收到同 `Message-Id` 的 MQ 消息（payload 与 `/logs` 的 `data.items[]` 单项一致）。

**限流**：管理 API 挂了 `CriticalRateLimit`（默认 20 次 / 20 分钟，按客户端 IP）。联调避免短时间狂打写接口；收到 `429` 等待窗口过期。

---

## 2. HMAC 认证（管理面与业务归属头共用）

### 2.1 请求头

| Header | 说明 |
|---|---|
| `X-Nova-Key-Id` | 可选。省略时使用配置里第一把 key（current）。单密钥部署可不传 |
| `X-Nova-Timestamp` | Unix 秒（UTC） |
| `X-Nova-Nonce` | 22–128 位，`[A-Za-z0-9_-]`，至少约 128 bit 熵；**不可重放** |
| `X-Nova-Signature` | HMAC-SHA256 结果的 lowercase hex（64 字符） |

时间窗默认 ±300 秒（`NOVA_HMAC_MAX_SKEW_SECONDS`）；nonce 默认保留 600 秒（`NOVA_NONCE_TTL_SECONDS`）。

### 2.2 Canonical Request

```text
METHOD\n
ESCAPED_PATH\n
CANONICAL_QUERY\n
TIMESTAMP\n
NONCE\n
SHA256_HEX(BODY)
```

| 行 | 规则 |
|---|---|
| `METHOD` | 大写，如 `POST` |
| `ESCAPED_PATH` | `url.EscapedPath()`；空则 `/` |
| `CANONICAL_QUERY` | query 各 key 的值排序后 `url.Encode()`；无 query 则为空字符串 |
| `TIMESTAMP` / `NONCE` | 与请求头原文一致 |
| `SHA256_HEX(BODY)` | 原始 body 的 SHA-256，小写 hex；无 body 时对空字节数组哈希（`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`） |

签名：`signature = hex( HMAC-SHA256(secret_bytes, canonical_string) )`，密钥为 base64url 解码后的 secret。

### 2.3 示例（Python，用本机密钥）

```python
import base64, hashlib, hmac, time, secrets, urllib.parse, urllib.request

BASE = "http://127.0.0.1:3000"
SECRET = base64.urlsafe_b64decode("I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk" + "==")

def canonical_query(query: str) -> str:
    if not query:
        return ""
    parsed = urllib.parse.parse_qs(query, keep_blank_values=True)
    items = []
    for key in sorted(parsed):
        for value in sorted(parsed[key]):
            items.append((key, value))
    return urllib.parse.urlencode(items, doseq=True)

def sign(method: str, path: str, query: str, body: bytes) -> dict:
    ts = str(int(time.time()))
    nonce = secrets.token_urlsafe(18)
    canonical = "\n".join([method.upper(), path or "/", canonical_query(query), ts, nonce,
                           hashlib.sha256(body).hexdigest()])
    sig = hmac.new(SECRET, canonical.encode(), hashlib.sha256).hexdigest()
    return {"X-Nova-Timestamp": ts, "X-Nova-Nonce": nonce, "X-Nova-Signature": sig}

headers = sign("GET", "/api/novapay/health", "", b"")
headers["Accept"] = "application/json"
req = urllib.request.Request(BASE + "/api/novapay/health", headers=headers)
print(urllib.request.urlopen(req).read().decode())
```

### 2.4 失败

任何 key / 时间窗 / nonce / 签名 / body 篡改问题均统一返回：

```json
{ "success": false, "code": "nova_authentication_failed", "message": "service authentication failed" }
```

HTTP `401`。不区分具体失败原因，避免信息泄露。

---

## 3. 管理 API

Base：`http://127.0.0.1:3000`。所有路由（含 `/health`）都要求 HMAC。

**统一响应格式**：

- 成功：`{ "success": true, "message": "ok", "data": ... }`
  - 租户列表的分页字段在 `data` 内（`total` / `page` / `page_size` / `items`）
  - 用量日志的列表在 `data.items`，`data.has_more` 表示本页条数是否等于 `size`
- 失败：`{ "success": false, "code": "...", "message": "...", "request_id": "..." }`

**标识规则**：

| 字段 | 规则 |
|---|---|
| `tenant_key` | `^[A-Za-z0-9][A-Za-z0-9._-]{2,19}$`（3–20，即 `users.username`，创建后不可变） |
| `tenant_name` / `display_name` | 最长 20 |
| `token_name` | `^[A-Za-z0-9][A-Za-z0-9._-]{0,49}$` |
| `request_id`（全部写接口） | `^[A-Za-z0-9._:-]{8,128}$` |
| `order_no`（租户调额） | 1–128，`[A-Za-z0-9._:-]` |
| 额度 | 整数，范围 `0 .. 2^53-1` |

**幂等（写接口）**：

- HTTP 层：`(scope, request_id)`。同 `request_id` + 同 body hash → 返回原结果，响应头 `Idempotency-Replayed: true`；同 `request_id` + 不同 body → `409` / `idempotency_conflict`。网络重试复用同一 `request_id` 即可。
- 租户调额另有业务防重：`nova_quota_operations` 对 `(tenant_key, order_no)` 唯一（`target_type=tenant`、`target_ref=''`）。同 `order_no` + 相同参数/`reason` → 不重复加减，body 带 `"replayed": true`；同 `order_no` + 不同参数 → `409` / `operation_conflict`。新调额必须用新 `order_no`。
- 令牌额度设置只依赖 `request_id`（无 `order_no`）。

### 3.1 路由一览

| 方法 | 路径 | body `request_id` | 用途 |
|---|---|---|---|
| `POST` | `/api/novapay/tenant` | 是 | 创建租户 + 专属用户 + 初始 Token |
| `GET` | `/api/novapay/tenant/{tenant_key}` | 否 | 查询租户 |
| `PUT` | `/api/novapay/tenant/{tenant_key}` | 是 | 更新显示名 |
| `DELETE` | `/api/novapay/tenant/{tenant_key}` | 是 | 软删除 |
| `GET` | `/api/novapay/tenants` | 否 | 分页列表；`keyword`、`status` 过滤 |
| `POST` | `/api/novapay/tenant/{tenant_key}/quota` | 是 | 调整租户余额（增量 / 绝对值） |
| `POST` | `/api/novapay/tenant/{tenant_key}/disable` | 是 | 禁用租户及全部 Token |
| `POST` | `/api/novapay/tenant/{tenant_key}/enable` | 是 | 启用租户（**不**自动启用曾单独禁用的 Token） |
| `POST` | `/api/novapay/tenant/{tenant_key}/token/rotate` | 是 | 密钥轮换 |
| `GET` | `/api/novapay/tenant/{tenant_key}/tokens` | 否 | 租户密钥列表（含完整 key） |
| `POST` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}/quota` | 是 | 令牌额度设置（`remain_quota` / `unlimited_quota`） |
| `DELETE` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}` | 是 | 吊销令牌（置 disabled） |
| `GET` | `/api/novapay/tenant/{tenant_key}/logs` | 否 | 成功用量账本 |
| `GET` | `/api/novapay/models` | 否 | 可用模型目录 |
| `GET` | `/api/novapay/health` | 否 | 健康检查 |

### 3.2 创建租户

`POST /api/novapay/tenant`

```json
{
  "tenant_key": "nova-test-4",
  "tenant_name": "示例科技有限公司",
  "initial_quota": 5000000,
  "request_id": "req-20260921-0001"
}
```

成功响应：

```json
{
  "success": true, "message": "ok",
  "data": {
    "user_id": 13, "username": "nova-test-4",
    "token_name": "nova-nova-test-4", "token_key": "sk-xxxxxxxx",
    "quota": 5000000
  }
}
```

- 默认 Token 名为 `nova-{tenant_key}`（超长截断到 50），`quota` 同时写入用户余额与初始 Token 额度。`token_key` 明文仅本次返回。
- 同 `request_id` + 同 body 重放 → 返回同一 `token_key`，带 `Idempotency-Replayed: true`；同 `request_id` + 不同 body → `409`。

### 3.3 查询租户

`GET /api/novapay/tenant/{tenant_key}`

```json
{
  "success": true, "message": "ok",
  "data": {
    "user_id": 12, "username": "nova-test-3", "display_name": "Nova Test Tenant",
    "status": "enabled", "quota": 894953, "used_quota": 105047,
    "token_name": "nova-nova-test-3", "token_key": "sk-DXql**********MfmB",
    "created_at": "2026-09-20T06:51:41Z", "last_active_at": "2026-09-21T07:50:53Z"
  }
}
```

- `status`：`enabled` / `disabled` / `deleted`。已软删租户仍返回 **200**，以 `data.status=deleted` 判断。
- `created_at` / `last_active_at` 为 RFC3339（UTC）；`last_active_at` 取最近成功用量与最近登录中较晚者。
- `token_key` 为掩码；完整明文只在创建 / 轮换 / 密钥列表接口返回。

### 3.4 租户列表

`GET /api/novapay/tenants?page=1&page_size=20&keyword=nova&status=enabled`

- `status` 仅 `enabled` / `disabled` / `deleted`；`keyword` 匹配 `username`、`display_name`（不区分大小写）。
- 分页字段在 `data` 内；`created_at` / `last_active_at` 为 Unix 秒；不返回 token 明文。

```json
{
  "success": true, "message": "ok",
  "data": {
    "total": 2, "page": 1, "page_size": 20,
    "items": [
      {
        "username": "nova-test-3", "user_id": 12, "display_name": "Nova Test Tenant",
        "status": "enabled", "quota": 894953, "used_quota": 105047,
        "created_at": 1789887101, "last_active_at": 1789976239
      }
    ]
  }
}
```

### 3.5 更新显示名

`PUT /api/novapay/tenant/{tenant_key}`

```json
{ "display_name": "新名称", "request_id": "req-20260921-0002" }
```

### 3.6 启用 / 禁用 / 软删除

均为 `POST /api/novapay/tenant/{tenant_key}/enable|disable` 与 `DELETE /api/novapay/tenant/{tenant_key}`，body 都带 `{"request_id":"..."}`。

- `disable`：用户与全部 Token 置 `disabled`。
- `enable`：仅恢复用户 `enabled`，**不会**恢复曾单独禁用的 Token。
- `DELETE`：**软删除**，`users.deleted_at` 置位，账本与历史保留；`GET` 仍返回 200 且 `status=deleted`。
- 已删除租户上的任何写操作（含再次 enable）→ `409` / `tenant_deleted`。

### 3.7 调整租户额度

`POST /api/novapay/tenant/{tenant_key}/quota`

增量模式（`delta_quota` 可为负）：

```json
{
  "request_id": "req-20260921-0003",
  "order_no": "R2026092100001",
  "delta_quota": 1000000,
  "reason": "recharge"
}
```

绝对值模式（与增量二选一）：

```json
{
  "request_id": "req-20260921-0004",
  "order_no": "R2026092100002",
  "absolute_quota": 6000000,
  "reason": "reconcile"
}
```

成功响应：

```json
{
  "success": true, "message": "ok",
  "data": {
    "username": "nova-test-3",
    "quota_before": 894953, "quota_after": 1894953,
    "delta_quota": 1000000, "order_no": "R2026092100001",
    "reason": "recharge", "replayed": false
  }
}
```

- `delta_quota` / `absolute_quota` 必须且只能传一个；不会扣到负余额，也不会超过钱包上限（`2^53-1`）。
- `reason` 必填（1–255）。`order_no` 防重说明见第 3 节开头。
- 修改的是**用户余额**（`users.quota`）；Token 自身的 `remain_quota` 用 3.10 的令牌额度接口。

### 3.8 密钥轮换

`POST /api/novapay/tenant/{tenant_key}/token/rotate`

```json
{ "reason": "疑似泄露", "request_id": "req-20260921-0005" }
```

```json
{
  "success": true, "message": "ok",
  "data": { "old_token_name": "nova-nova-test-3", "token_name": "nova-nova-test-3", "token_key": "sk-xxxxxxxx" }
}
```

就地轮换主令牌密钥，令牌名与额度不变；旧 `sk-...` 立即失效。新明文仅在首次响应与同 `request_id` 重放时返回。`reason` 必填。

### 3.9 租户密钥列表

`GET /api/novapay/tenant/{tenant_key}/tokens`

```json
{
  "success": true, "message": "ok",
  "data": {
    "items": [
      {
        "token_id": 7, "token_name": "nova-nova-test-3", "key": "sk-xxxxxxxx",
        "status": "enabled", "expired_time": -1,
        "remain_quota": 4896554, "unlimited_quota": false, "used_quota": 103446,
        "created_time": 1789887101
      }
    ]
  }
}
```

- 返回该租户全部密钥，`key` 为**完整明文**（仅管理面 HMAC 可见）。
- `status`：`enabled` / `disabled` / `expired` / `exhausted`；`expired_time` 为 Unix 秒，`-1` 表示永不过期。

### 3.10 令牌额度设置

`POST /api/novapay/tenant/{tenant_key}/tokens/{token_name}/quota`

```json
{
  "remain_quota": 500000,
  "unlimited_quota": false,
  "reason": "员工额度调整",
  "request_id": "req-20260921-0006"
}
```

```json
{
  "success": true, "message": "ok",
  "data": { "token_name": "nova-nova-test-3", "remain_quota": 500000, "unlimited_quota": false }
}
```

直接设置该 Token 的 `remain_quota` / `unlimited_quota`（`unlimited_quota=false` 时按 Token 扣费）。若 Token 因额度耗尽为 `exhausted`，设置后额度可用时自动恢复 `enabled`。幂等仅依赖 `request_id`。

### 3.11 吊销令牌

`DELETE /api/novapay/tenant/{tenant_key}/tokens/{token_name}`，body `{"request_id":"..."}`。将该令牌置 `disabled`。

### 3.12 用量日志

`GET /api/novapay/tenant/{tenant_key}/logs?last_id=0&size=100&start_time=1700000000&end_time=1700086400`

返回成功用量账本（基于 New-API 消费日志，非事件快照）。没有 `total`、`page`、`page_size`。按账本 `id` 升序。`has_more` 为 true 时，用本页最后一条的 `id` 作为下次 `last_id`。满页也会是 true，即使后面已经没有记录；再请求一次得到空列表即可停止。

| 参数 | 必填 | 含义 |
|---|---|---|
| `last_id` | 否 | 账本行 `id`。只返回 `id` 更大的记录；省略或 `0` 从最早开始 |
| `size` | 否 | 每页条数，1–100，默认 100。本页条数等于 `size` 时 `has_more=true` |
| `start_time` | 否 | Unix 秒，含本值。筛选账本 `occurred_at` |
| `end_time` | 否 | Unix 秒，含本值。与 `start_time`、`last_id` 同时生效 |

不要用 `log.id` 做 `last_id`。消费日志可能先落库，账本行后补，按 `log.id` 续拉会漏。对账仍用 `log.id`。

```json
{
  "success": true, "message": "ok",
  "data": {
    "has_more": false,
    "items": [
      {
        "id": 15,
        "tenant_key": "nova-test-3",
        "user_id": 12,
        "request_id": "202609210737189217072858268d9d6lQyl2gto",
        "nova_request_id": "nova-test-3-video-7379fd44-1ffe-4154-9eb7-e9ee974f843f",
        "model_type": "video",
        "log": {
          "id": 111, "type": 2, "model_name": "wanx2.1-t2v-turbo",
          "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
          "created_at": "2026-09-21T07:37:19Z", "is_stream": false,
          "token_id": 7, "token_name": "nova-nova-test-3",
          "channel_id": 1, "channel_name": "...", "group": "default"
        },
        "quota_data": {
          "quota": 86012, "created_time": "2026-09-21T07:37:19Z",
          "deducted_amount_usd": 0.172024,
          "model_price": 0, "group_ratio": 1, "matched_tier": "video",
          "billing_mode": "tiered_expr", "usage_facts": { "resolution": "480P", "seconds": 5 }
        },
        "other": {
          "request_path": "/v1/videos", "task_id": "task_...", "is_task": true,
          "content": "..."
        }
      }
    ]
  }
}
```

字段说明：

- `id`：账本行主键，只用于 `last_id` 续拉。
- `model_type`：`text` / `image` / `video`（由请求路径推断）。
- `log`：消费日志字段（token、渠道、耗时、`upstream_request_id`、`ip` 等）；**计费相关字段在 `quota_data`**。`log.id` 是对账唯一键。
- `quota_data`：扣费金额、倍率、计费模式、分档快照等计费字段；`deducted_amount_usd = quota / QuotaPerUnit`。
- `other`：其余非敏感扩展字段；角色级/内部字段（`admin_info`、`root_info`、`audit_info`、内部计费表达式等）不返回。
- `request_id` / `nova_request_id`：业务请求的 `X-Nova-Request-Id` 落在 `nova_request_id`；`request_id` 为 New-API 自身请求号，无归属头时两者回退为同一值。

### 3.13 模型目录

`GET /api/novapay/models`

仅返回当前可路由的模型（有启用渠道）。不返回渠道密钥、上游地址或渠道名。

```json
{
  "success": true, "message": "ok",
  "data": {
    "items": [
      {
        "model_name": "deepseek-r1", "channel_count": 1, "enabled": true,
        "vendor_name": "DeepSeek",
        "quota_type": 0, "model_ratio": 37.5, "completion_ratio": 1,
        "enable_groups": ["default"], "supported_endpoint_types": ["openai"]
      },
      {
        "model_name": "qwen-image-plus", "channel_count": 1, "enabled": true,
        "quota_type": 1, "model_price": 0.028671,
        "enable_groups": ["default"], "supported_endpoint_types": ["image-generation", "openai"]
      }
    ]
  }
}
```

- `quota_type`：`0` 按 token 倍率（带 `model_ratio` / `completion_ratio`），`1` 按次计价（带 `model_price`）。
- `description` / `tags` / `vendor_name` / `cache_ratio` 等仅在配置了对应元数据时出现。

### 3.14 健康检查

`GET /api/novapay/health`

```json
{
  "success": true, "message": "ok",
  "data": {
    "status": "up",
    "database": { "connected": true },
    "redis": { "enabled": true, "connected": true },
    "mq": { "connected": true, "outbox_pending": 0, "outbox_dead": 0 }
  }
}
```

- `status`：`up` / `degraded` / `down`。主库不可用 → `down`；主库可用但 MQ 未连通、或 Redis 启用但不可达 → `degraded`；均正常 → `up`（Redis 未启用时忽略）。
- 建议告警：`database.connected == false`、`mq.connected == false`、`mq.outbox_dead > 0` 立即告警；`outbox_pending > 1000` 告警。

### 3.15 错误码

| HTTP | code | 场景 |
|---|---|---|
| 400 | `invalid_request` | body 校验失败、`request_id`/`order_no` 非法、`delta_quota` 与 `absolute_quota` 同时传或都不传 |
| 400 | `invalid_quota` | 额度超出范围 |
| 401 | `nova_authentication_failed` | HMAC 校验失败 |
| 403 | `nova_tenant_unavailable` | 业务请求中租户被禁用/已删除 |
| 404 | `tenant_not_found` / `token_not_found` | 租户或令牌不存在 |
| 409 | `tenant_exists` | 创建租户时重名 |
| 409 | `tenant_deleted` | 对已删除租户执行写操作 |
| 409 | `idempotency_conflict` | 同 `request_id` + 不同 body |
| 409 | `operation_conflict` | 同 `order_no` + 不同调额参数 |
| 409 | `operation_in_progress` | 同 `request_id` 并发在途 |
| 409 | `quota_out_of_range` | 调额将超出允许范围 |

---

## 4. 业务请求（调用模型 / 任务）

非 Nova Token 行为不变。Token 属于某 Nova 租户时，业务请求**除 `Authorization` 外额外要求**：

| Header | 说明 |
|---|---|
| `X-Nova-Tenant-Key` | 必须等于该 Token 的租户 `tenant_key` |
| `X-Nova-Request-Id` | 建议携带，用于用量归属（展示在 `/logs` 的 `nova_request_id`） |
| `X-Nova-Timestamp` / `X-Nova-Nonce` / `X-Nova-Signature` | 对**实际业务请求**的 METHOD / PATH / QUERY / BODY 签名（与管理 API 同算法） |

### 4.1 示例

```http
POST /v1/chat/completions HTTP/1.1
Host: 127.0.0.1:3000
Authorization: Bearer sk-DXql71klnsJwizWBL6ZXp2oykxYoilgKLkfPOdPPmeoiMfmB
Content-Type: application/json
X-Nova-Timestamp: <unix秒>
X-Nova-Nonce: ...
X-Nova-Signature: ...
X-Nova-Tenant-Key: nova-test-3
X-Nova-Request-Id: nova-req-<uuid>

{
  "model": "deepseek-r1",
  "messages": [{"role": "user", "content": "只回复ok"}],
  "max_tokens": 8,
  "stream": false
}
```

### 4.2 校验逻辑

1. Token 属于 Nova 租户时，该租户必须存在且为 `enabled`；租户被禁用/已删除 → `403` / `nova_tenant_unavailable`。
2. HMAC 签名必须有效，且 `X-Nova-Tenant-Key` 必须等于该 Token 的租户；任一不满足 → `401` / `nova_authentication_failed`（用例不会被归属到错误租户）。

### 4.3 用量上报条件

| 场景 | 是否上报 | `source.type` / `source.key` |
|---|---|---|
| 同步 relay 最终结算成功 | 是 | `relay` / New-API request id |
| 通用异步 Task 最终成功且结算完成 | 是 | `task` / `task_id` |
| 预扣、失败、退款、进行中 | 否 | — |

仅上报**最终成功且已结算**的用量；`quota == 0` 的成功请求默认仍发布，便于审计。

---

## 5. RabbitMQ 用量消息

### 5.1 参数

| 项 | 值 |
|---|---|
| Exchange | `nova.events`（topic，durable） |
| Routing Key | `nova.usage.reported` |
| Delivery Mode | persistent |
| Content-Type | `application/json` |
| Message-Id | 等于 `event_id` |
| 消费队列 | **测试方自行声明**并绑定上述 exchange + routing key；New-API 不代声明 |
| 去重键 | AMQP `Message-Id`（等于 `event_id`） |

本机实测消息样例（payload 与 `/logs` 的 `data.items[]` 单项同结构，不含 `has_more`；幂等键在 AMQP `Message-Id`）：

```json
{
  "id": 15,
  "tenant_key": "nova-test-3",
  "user_id": 12,
  "request_id": "202609210737189217072858268d9d6lQyl2gto",
  "nova_request_id": "nova-test-3-video-7379fd44-1ffe-4154-9eb7-e9ee974f843f",
  "model_type": "video",
  "log": {
    "id": 111, "type": 2, "model_name": "wanx2.1-t2v-turbo",
    "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
    "created_at": "2026-09-21T07:37:19Z", "is_stream": false, "use_time": 0,
    "token_id": 7, "token_name": "nova-nova-test-3",
    "channel_id": 1, "channel_name": "...", "group": "default"
  },
  "quota_data": {
    "quota": 86012, "created_time": "2026-09-21T07:37:19Z",
    "deducted_amount_usd": 0.172024,
    "model_price": 0, "group_ratio": 1, "matched_tier": "video",
    "billing_mode": "tiered_expr", "expr": "tier(\"video\", u(\"seconds\") * 0.034405)",
    "usage_facts": { "resolution": "480P", "seconds": 5 }
  },
  "other": {
    "request_path": "/v1/videos", "task_id": "task_...", "is_task": true,
    "content": "..."
  }
}
```

字段规则：

- payload 字段与 `GET .../logs` 的 `data.items[]` 单项一致（见 3.12），含账本 `id`；不含 `has_more`，也不含旧的 `schema_version` / `event_id` / `event_type` / `usage` / `context`。
- 幂等去重使用 AMQP `Message-Id`（等于 `nova_log_ref.event_id` / outbox `event_id`）。
- 不携带完整 Token、HMAC secret、`Authorization`、`admin_info` 等敏感字段。

### 5.2 消费要求（强制）

1. 以 AMQP `Message-Id`（`event_id`）做幂等去重（数据库唯一约束或等价机制）；重复投递应 ACK 且不重复记账。
2. 可用 `(request_id, nova_request_id, tenant_key)` 作业务侧辅助核对，权威去重键仍是 `Message-Id`。
3. 处理成功再 ACK；失败 NACK/重回队列，避免静默丢账。

### 5.3 对账与补偿

- `nova_log_ref` 表 + Outbox 与记账**同事务**写入；MQ 故障不回滚已成功的调用与计费，事件在 outbox 积压，MQ 恢复后自动投递。
- 有 `outbox_dead > 0` 需运维介入（检查 MQ、权限、消息体）。
- `nova_attributions` 是结算前的归属候选，成功落账或请求 4xx/5xx 后自动清理。**禁止盲目自动补发**：候选可能表示"请求仍在途"或"已计费但事件写入失败"。
- 对账以 `/api/novapay/.../logs` 或 MQ 消费结果为准，差异时按 `Message-Id` / `request_id` / `nova_request_id` 排查；积压看 `/health` 的 `outbox_pending` / `outbox_dead`。

---

## 6. 对接检查清单

- [ ] HMAC：正确签名可通过；错误签名 / 过期时间 / 重放 nonce / 篡改 body 均 `401`
- [ ] 创建租户拿到 `token_key`；同 `request_id` 重放返回同一 `token_key`
- [ ] 租户调额：同 `request_id` 重试不重复；同 `order_no` 换 `request_id` 也不重复；新单号才再次生效
- [ ] 软删后 `GET` 仍 200 且 `status=deleted`；再启用/修改返回 `409` `tenant_deleted`
- [ ] 管理面约 20 次写操作后出现 `429`，约 20 分钟窗口后恢复
- [ ] 用本机 Token + 归属头成功调用 `POST /v1/chat/completions`（`deepseek-r1`）
- [ ] 错误 `X-Nova-Tenant-Key` 被拒绝（`401`）；Nova Token 不带 Nova 头也被拒绝
- [ ] 成功后 `/logs` 出现记录，且 RabbitMQ 收到同 `Message-Id` 消息，payload 与 `/logs` 的 `data.items[]` 单项一致
- [ ] 故意重复投递同一消息，消费端不重复记账
- [ ] `/health` 在主库与 MQ 正常时为 `data.status=up`

---

## 7. 安全注意

1. 第 1 节密钥与 Token **仅限本机联调**；生产必须轮换并改走 TLS / AMQPS。
2. Secret / Token 明文不得写入业务日志、MQ payload、URL、前端。
3. 密钥轮换：先加 previous key，再切 current；移除 previous 前须超过最大时间窗与在途请求时长。
4. 写接口务必使用唯一 `request_id`（建议 UUID）。

版本说明：本文档随 `integration/nova` 实现同步更新；接口或消息体变更时应提前约定兼容窗口。