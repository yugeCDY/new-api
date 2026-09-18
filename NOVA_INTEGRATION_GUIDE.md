# New-API ↔ Nova 对接指南

面向 **Nova 平台开发人员 / 联调 Agent** 的对接说明。覆盖管理 API、HMAC 认证、业务请求归属头、RabbitMQ 用量消息与健康检查。

| 项 | 值 |
|---|---|
| 文档版本 | 1.3（额度幂等：HTTP Key 与业务 `operation_id` 解耦） |
| 对应实现 | New-API Nova 集成（`integration/nova`） |
| 管理 API 前缀 | `/api/novapay` |
| MQ Exchange | `nova.events`（topic，durable） |
| 用量 Routing Key | `nova.usage.reported` |
| 消息 Schema | `schema_version: 1` |
| 交付语义 | **at-least-once**（消费端必须按 `event_id` 去重） |

---

## 0. 本机联调环境（当前可用，可直接测）

> 以下为 **本机当前运行中的 New-API + RabbitMQ** 实参。其他 Agent **只需依据本节 + 后文协议**即可完成联调，不必再翻代码。  
> 仅限本地测试；**勿用于生产**。

### 0.1 服务地址

| 用途 | 地址 |
|---|---|
| New-API Base URL | `http://127.0.0.1:3000` |
| 管理后台（浏览器） | http://127.0.0.1:3000 |
| RabbitMQ AMQP | `amqp://admin:admin123@127.0.0.1:5672/` |
| RabbitMQ 管理台 | http://127.0.0.1:15672 （用户 `admin` / 密码 `admin123`） |
| vhost | `/` |

确认服务存活：

```bash
curl -s http://127.0.0.1:3000/api/status
# 期望 success=true

# 健康检查必须带 HMAC（见 0.2）；裸请求会 401
```

### 0.2 HMAC 密钥（当前生效）

| 项 | 值 |
|---|---|
| Key Id | `current` |
| Secret（base64url，已去 padding） | `I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk` |
| 服务端环境变量等价写法 | `NOVA_HMAC_KEYS=current:I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk` |

Python 解码 secret：

```python
import base64
SECRET = base64.urlsafe_b64decode("I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk" + "==")
# len(SECRET) == 32
```

请求头固定：

```http
X-Nova-Key-Id: current
X-Nova-Timestamp: <unix秒>
X-Nova-Nonce: <22-128位 URL-safe>
X-Nova-Signature: <HMAC-SHA256 lowercase hex>
```

### 0.3 已有测试租户（可直接调用业务接口）

| 项 | 值 |
|---|---|
| `tenant_key` | `e2e-full-20260918150511` |
| Token 名称 | `live` |
| Bearer Token | `sk-4xTTb2l6uBAJLLy2djr8qfMFTxaRs4ABhuAxaCSDmqopzBQn` |
| 租户状态 | `enabled` |

业务调用示例模型（本机渠道已配置）：

| 模型 | 说明 |
|---|---|
| `deepseek-v3` | 推荐联调用 |
| `deepseek-v4-flash` | 亦可用 |

业务请求除 `Authorization` 外必须额外带：

```http
X-Nova-Tenant-Key: e2e-full-20260918150511
X-Nova-Request-Id: <任意唯一字符串>
```

以及完整 HMAC 四件套（对 `/v1/chat/completions` 的 METHOD/PATH/QUERY/BODY 签名）。

### 0.4 RabbitMQ 订阅参数

| 项 | 值 |
|---|---|
| Exchange | `nova.events`（已存在，type=`topic`，durable） |
| Routing Key | `nova.usage.reported` |
| 消费队列 | **测试方自行声明**（例如 `nova.test.consumer`），绑定到上述 exchange + routing key |
| 去重键 | 消息 JSON 的 `event_id`（也等于 AMQP `message_id`） |

管理台快速验消息：http://127.0.0.1:15672 → Exchanges → `nova.events`，或自建队列 Get Message。

### 0.5 推荐最小验证顺序（给联调 Agent）

1. **HMAC 健康检查**  
   `GET http://127.0.0.1:3000/api/novapay/health` + 正确签名 → `200` 且 `status=ok`，`rabbitmq.connected=true`。  
   故意改坏 `X-Nova-Signature` → `401` / `nova_authentication_failed`。

2. **管理面抽查**  
   `GET /api/novapay/tenant/e2e-full-20260918150511`  
   `GET /api/novapay/tenant/e2e-full-20260918150511/logs?page=1&page_size=20`  
   （带 query 时，签名 canonical 的 query 段必须与实际 query 一致。）

3. **业务成功路径**  
   `POST /v1/chat/completions`，body 使用 `deepseek-v3`，Bearer + HMAC + `X-Nova-Tenant-Key` + `X-Nova-Request-Id` → 期望 `200`。

4. **账本 + MQ**  
   - `/logs` 出现新 `event_id`  
   - 自建队列收到同 `event_id`、`event_type=nova.usage.reported`、`schema_version=1` 的消息

5. **负面**  
   - 错误 `X-Nova-Tenant-Key` → 401  
   - Nova Token 不带 Nova 头 → 401  

### 0.6 限流注意

管理面挂了 `CriticalRateLimit`：**约 20 次 / 20 分钟**（按客户端 IP）。联调时避免短时间狂打写接口；若收到 `429`，等待窗口过期或联系本机维护者清理 Redis 限流键后再试。

### 0.7 一键环境变量（复制即用）

```bash
export NOVA_BASE_URL=http://127.0.0.1:3000
export NOVA_KEY_ID=current
export NOVA_HMAC_SECRET=I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk
export NOVA_TENANT_KEY=e2e-full-20260918150511
export NOVA_TOKEN=sk-4xTTb2l6uBAJLLy2djr8qfMFTxaRs4ABhuAxaCSDmqopzBQn
export NOVA_TEST_MODEL=deepseek-v3
export RABBITMQ_URL=amqp://admin:admin123@127.0.0.1:5672/
export RABBITMQ_MGMT=http://127.0.0.1:15672
export RABBITMQ_USER=admin
export RABBITMQ_PASSWORD=admin123
```

PowerShell：

```powershell
$env:NOVA_BASE_URL='http://127.0.0.1:3000'
$env:NOVA_KEY_ID='current'
$env:NOVA_HMAC_SECRET='I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk'
$env:NOVA_TENANT_KEY='e2e-full-20260918150511'
$env:NOVA_TOKEN='sk-4xTTb2l6uBAJLLy2djr8qfMFTxaRs4ABhuAxaCSDmqopzBQn'
$env:NOVA_TEST_MODEL='deepseek-v3'
$env:RABBITMQ_URL='amqp://admin:admin123@127.0.0.1:5672/'
$env:RABBITMQ_MGMT='http://127.0.0.1:15672'
$env:RABBITMQ_USER='admin'
$env:RABBITMQ_PASSWORD='admin123'
```

---

## 1. 总体架构

```text
Nova 平台
  ├─ HMAC 管理请求 ───────────────> http://127.0.0.1:3000/api/novapay/*
  ├─ Bearer Token + HMAC 归属头 ──> http://127.0.0.1:3000/v1/*、通用任务接口
  └─ 消费 RabbitMQ <─────────────── amqp://127.0.0.1:5672  /  nova.events / nova.usage.reported
```

要点：

1. **管理面**：Nova 通过 HMAC 调用 `/api/novapay/*` 管理租户、额度、Token、日志与健康状态。
2. **业务面**：终端用户仍用 New-API Token（`Authorization: Bearer sk-...`）调用模型；Nova 额外附加经 HMAC 签名的归属头，与 Token 所有权双重校验。
3. **用量面**：仅上报**最终成功**且已结算的实际用量；预扣、失败、退款、进行中状态不上报。
4. **开关**：New-API 侧 `NOVA_INTEGRATION_ENABLED=false` 时，管理路由不暴露，行为与未接入 Nova 一致。本机当前为 **已开启**。

---

## 2. 联调前准备

本机已就绪时可直接使用 **第 0 节**。通用约定如下：

| 配置 | 本机当前值 |
|---|---|
| New-API Base URL | `http://127.0.0.1:3000` |
| `NOVA_INTEGRATION_ENABLED` | `true` |
| `NOVA_HMAC_KEYS` | `current:I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk` |
| RabbitMQ | `amqp://admin:admin123@127.0.0.1:5672/`；exchange `nova.events` 已声明 |
| TLS | 本地 HTTP / 明文 AMQP（仅开发） |

### 2.1 HMAC 密钥约定

- 环境变量：`NOVA_HMAC_KEYS`
- 格式：`key-id:base64url-secret[,previous-key-id:base64url-secret]`
- 首项为**当前 key**，最多再保留一把**轮换期旧 key**
- 每个 secret Base64URL 解码后长度 **≥ 32 字节**
- 响应不回传 secret；签名比对使用常量时间比较

本机当前（单 key，无 previous）：

```text
NOVA_HMAC_KEYS=current:I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk
```

时间窗默认 ±300 秒（`NOVA_HMAC_MAX_SKEW_SECONDS`）；nonce 默认保留 600 秒。

---

## 3. HMAC 签名（管理 API 与归属头共用）

### 3.1 请求头

| Header | 说明 |
|---|---|
| `X-Nova-Key-Id` | 本机填 `current` |
| `X-Nova-Timestamp` | Unix 秒（UTC） |
| `X-Nova-Nonce` | 22–128 字符，`[A-Za-z0-9_-]`；至少约 128 bit 熵 |
| `X-Nova-Signature` | HMAC-SHA256 结果的 **lowercase hex**（64 字符） |

业务请求额外需要（见第 5 节）：

| Header | 本机示例 |
|---|---|
| `X-Nova-Tenant-Key` | `e2e-full-20260918150511` |
| `X-Nova-Request-Id` | 任意唯一 ID，如 `nova-req-<uuid>` |

### 3.2 Canonical String

```text
METHOD\n
ESCAPED_PATH\n
CANONICAL_QUERY\n
TIMESTAMP\n
NONCE\n
SHA256_HEX(BODY)
```

规则：

- `METHOD`：大写，如 `POST`
- `ESCAPED_PATH`：`url.EscapedPath()`；空则 `/`
- `CANONICAL_QUERY`：query 按 key 编码，同 key 多值先排序再 `Encode()`；无 query 则为空字符串
- `TIMESTAMP` / `NONCE`：与请求头原文一致
- `SHA256_HEX(BODY)`：原始 body 的 SHA-256，**小写 hex**；无 body 时对空字节数组哈希  
  （空 body 的 SHA-256 hex 为 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`）

### 3.3 签名算法

```text
signature = hex_encode( HMAC-SHA256(secret_bytes, canonical_string) )
```

本机 `secret_bytes` = Base64URL 解码 `I-66qs2nySY-TOMGme6pD3to1nUEViopwRZ8fQzwLhk`（需补 `=` padding）。

### 3.4 伪代码示例（Python，已对本机密钥）

```python
import base64, hashlib, hmac, time, secrets, urllib.parse, urllib.request, json

BASE = "http://127.0.0.1:3000"
KEY_ID = "current"
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
    body_hash = hashlib.sha256(body).hexdigest()
    canonical = "\n".join([
        method.upper(),
        path or "/",
        canonical_query(query),
        ts,
        nonce,
        body_hash,
    ])
    sig = hmac.new(SECRET, canonical.encode("utf-8"), hashlib.sha256).hexdigest()
    return {
        "X-Nova-Key-Id": KEY_ID,
        "X-Nova-Timestamp": ts,
        "X-Nova-Nonce": nonce,
        "X-Nova-Signature": sig,
    }

# 健康检查示例
headers = sign("GET", "/api/novapay/health", "", b"")
headers["Accept"] = "application/json"
req = urllib.request.Request(BASE + "/api/novapay/health", headers=headers)
print(urllib.request.urlopen(req).read().decode())
```

### 3.5 认证失败

统一返回 HTTP `401`：

```json
{
  "success": false,
  "code": "nova_authentication_failed",
  "message": "service authentication failed"
}
```

不区分 key / 时间窗 / nonce / 签名失败原因。nonce **不可重放**（服务端原子占用）。

---

## 4. 管理 API（`/api/novapay`）

Base：`http://127.0.0.1:3000`

- 全部需要 HMAC（含 `/health`）
- **写操作**必须带 `Idempotency-Key`：`^[A-Za-z0-9._:-]{8,128}$`
- HTTP 幂等维度是 **`(scope, Idempotency-Key)`**：
  - **同一 scope + 同一 Idempotency-Key + 同一 body hash** → 返回原结果，响应头 `Idempotency-Replayed: true`
  - **同一 scope + 同一 Idempotency-Key + 不同 body hash** → `409` / `idempotency_conflict`
  - 额度接口 scope **不含** `operation_id`：`tenant_quota:{tenant_key}`、`token_quota:{tenant_key}:{token_name}`
- **额度业务防重**另有一层：表 `nova_quota_operations` 对 `(tenant_key, target_type, target_ref, operation_id)` 唯一
  - 同一 `operation_id` + 相同 `delta`/`reason`（即使换了新的 `Idempotency-Key`）→ 不重复加减，body 带 `"replayed": true`
  - 同一 `operation_id` + 不同 `delta`/`reason` → `409` / `operation_conflict`
- 分页：`page`（默认 1）、`page_size`（默认 20，最大 100）
- 额度单位：New-API **quota 整数**（非美元）；范围 `0 .. 2^53-1`

### 4.1 路由一览

| 方法 | 路径 | 幂等头 | 用途 |
|---|---|---|---|
| `POST` | `/api/novapay/tenant` | 是 | 创建租户 + 专属用户 + 初始 Token |
| `GET` | `/api/novapay/tenant/{tenant_key}` | 否 | 查询租户 |
| `PUT` | `/api/novapay/tenant/{tenant_key}` | 是 | 更新显示名 / metadata |
| `DELETE` | `/api/novapay/tenant/{tenant_key}` | 是 | 软删除（不可再启用） |
| `GET` | `/api/novapay/tenants` | 否 | 分页列表 |
| `POST` | `/api/novapay/tenant/{tenant_key}/quota` | 是 | 调整租户用户余额 |
| `POST` | `/api/novapay/tenant/{tenant_key}/disable` | 是 | 禁用租户及全部 Token |
| `POST` | `/api/novapay/tenant/{tenant_key}/enable` | 是 | 启用租户（**不**自动启用曾单独禁用的 Token） |
| `POST` | `/api/novapay/tenant/{tenant_key}/token/rotate` | 是 | 轮换 Token，明文只返回一次 |
| `GET` | `/api/novapay/tenant/{tenant_key}/tokens` | 否 | Token 列表（仅掩码） |
| `POST` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}/quota` | 是 | 调整 Token 额度 |
| `DELETE` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}` | 是 | 吊销 Token |
| `GET` | `/api/novapay/tenant/{tenant_key}/logs` | 否 | 成功用量账本 |
| `GET` | `/api/novapay/models` | 否 | 可用模型 / 定价视图 |
| `GET` | `/api/novapay/health` | 否 | 模块 / DB / MQ / 积压 |

本机可先测（只读，少触发限流）：

- `GET http://127.0.0.1:3000/api/novapay/health`
- `GET http://127.0.0.1:3000/api/novapay/tenant/e2e-full-20260918150511`
- `GET http://127.0.0.1:3000/api/novapay/tenant/e2e-full-20260918150511/logs?page=1&page_size=20`
- `GET http://127.0.0.1:3000/api/novapay/models`

### 4.2 标识规则

| 字段 | 规则 |
|---|---|
| `tenant_key` | `^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`，创建后不可变 |
| `token_name` | `^[A-Za-z0-9][A-Za-z0-9._-]{0,49}$` |
| `operation_id`（额度调整） | 1–128，`[A-Za-z0-9._:-]` |
| `display_name` | 最长 128 |

### 4.2.1 软删除语义（已实测）

- `DELETE /api/novapay/tenant/{tenant_key}` 为**软删除**：账本与历史保留。
- 之后 `GET /api/novapay/tenant/{tenant_key}` 仍返回 **HTTP 200**，body 中 `status=deleted`（**不是 404**）。
- 客户端应以 `status` 字段判断是否可用。
- 对已删除租户再 `enable` / `update` / 调额等写操作 → **409** / `tenant_deleted`。

### 4.3 创建租户

`POST http://127.0.0.1:3000/api/novapay/tenant`

```json
{
  "tenant_key": "agent-test-001",
  "display_name": "Agent Test",
  "quota": 1000000,
  "token_name": "default",
  "token_quota": 1000000,
  "unlimited_quota": false,
  "metadata": { "biz": "optional" }
}
```

成功响应（首次）：

```json
{
  "success": true,
  "tenant": {
    "tenant_key": "agent-test-001",
    "display_name": "Agent Test",
    "status": "enabled",
    "user_id": 123,
    "quota": 1000000,
    "created_at": 1726646400,
    "updated_at": 1726646400
  },
  "token": {
    "name": "default",
    "token": "sk-xxxxxxxx",
    "masked_token": "sk-****xxxx",
    "status": 1,
    "remain_quota": 1000000,
    "unlimited_quota": false,
    "created_at": 1726646400,
    "secret_visible": true
  }
}
```

**重要：**

- `token` 明文只在**首次成功响应**出现；请立即安全存储。
- 幂等重放不会再次返回明文，而是 `secret_visible: false` 并提示仅首次可见。
- 列表 / 查询接口只返回 `masked_token`。
- 若不想新建租户，直接使用第 0.3 节已有租户与 Token。

### 4.4 调整额度

`POST http://127.0.0.1:3000/api/novapay/tenant/{tenant_key}/quota`

```json
{
  "operation_id": "op-20260918-001",
  "delta": 50000,
  "reason": "topup"
}
```

- `delta` 可为负（扣减）；不会把余额扣到允许范围以下。
- Token 额度：`POST .../tokens/{token_name}/quota`，body 相同。
- **两层防重（方案 1+2）**：
  | 层 | 作用 | 行为 |
  |---|---|---|
  | `Idempotency-Key` | HTTP 超时重试 | 同 Key 同 body → 原响应 + `Idempotency-Replayed: true`；同 Key 不同 body → `409 idempotency_conflict` |
  | `operation_id` | 业务单号（库唯一） | 同 `operation_id` 同 payload → 不重复加减，`"replayed": true`；同 `operation_id` 不同 payload → `409 operation_conflict` |
  - 网络重试：复用同一 `Idempotency-Key` 即可。
  - 业务重试 / 换 Key 再发：只要 `operation_id` 与 payload 不变，也不会二次加减。
  - 新的一笔调额：必须使用**新的** `operation_id`（并建议新的 `Idempotency-Key`）。

### 4.5 轮换 Token

`POST http://127.0.0.1:3000/api/novapay/tenant/{tenant_key}/token/rotate`

```json
{
  "old_token_name": "live",
  "new_token_name": "live-v2",
  "token_quota": 1000000,
  "unlimited_quota": false
}
```

- 创建新 Token 并吊销旧 Token。
- 新明文同样只返回一次。
- **注意**：轮换后第 0.3 节的旧 `sk-...` 会立即失效，需改用新明文。

### 4.6 用量日志

`GET http://127.0.0.1:3000/api/novapay/tenant/e2e-full-20260918150511/logs?page=1&page_size=20`

返回 Nova **事件账本**（非 `logs` / `quota_data` 表），字段包括：

`event_id`、`source_type`（`relay` / `task`）、`source_key`、`tenant_key`、`quota`、`model_name`、`prompt_tokens`、`completion_tokens`、`total_tokens`、`request_id`、`nova_request_id`、`occurred_at` 等。

### 4.7 健康检查

`GET http://127.0.0.1:3000/api/novapay/health`

期望示例：

```json
{
  "success": true,
  "status": "ok",
  "components": {
    "database": { "ok": true },
    "outbox": {
      "pending": 0,
      "dead": 0,
      "oldest_pending_at": 0
    },
    "usage_candidates": {
      "unresolved": 0,
      "oldest_created_at": 0
    },
    "rabbitmq": {
      "configured": true,
      "connected": true
    }
  },
  "timestamp": "2026-09-18T00:00:00Z"
}
```

建议告警：

| 条件 | 建议 |
|---|---|
| `outbox.dead > 0` | 立即告警 |
| 最老 pending 超过 5 分钟 | 告警 |
| `usage_candidates.unresolved` 长时间不清零 | 告警并人工核对（见第 7 节） |
| `status` 为 `degraded` / `unavailable` | 告警 |

### 4.8 通用错误体

```json
{
  "success": false,
  "code": "tenant_not_found",
  "message": "tenant not found",
  "request_id": "..."
}
```

常见 `code`：`invalid_request`、`invalid_quota`、`invalid_idempotency_key`、`idempotency_conflict`、`operation_conflict`、`operation_in_progress`、`quota_out_of_range`、`tenant_not_found`、`tenant_exists`、`tenant_deleted`、`nova_authentication_failed`、`nova_tenant_unavailable`、`internal_error`。

---

## 5. 业务请求（调用模型 / 任务）

### 5.1 本机可直接调用的请求头

```http
POST /v1/chat/completions HTTP/1.1
Host: 127.0.0.1:3000
Authorization: Bearer sk-4xTTb2l6uBAJLLy2djr8qfMFTxaRs4ABhuAxaCSDmqopzBQn
Content-Type: application/json
X-Nova-Key-Id: current
X-Nova-Timestamp: <unix秒>
X-Nova-Nonce: ...
X-Nova-Signature: ...
X-Nova-Tenant-Key: e2e-full-20260918150511
X-Nova-Request-Id: nova-req-<uuid>
```

Body 示例：

```json
{
  "model": "deepseek-v3",
  "messages": [{"role": "user", "content": "只回复ok"}],
  "max_tokens": 8,
  "stream": false
}
```

签名 canonical 基于**实际业务请求**的 METHOD / PATH / QUERY / BODY（与管理 API 相同算法）。PATH 为 `/v1/chat/completions`。

### 5.2 校验逻辑

1. Token 必须属于某个 Nova 租户且租户为 `enabled`
2. HMAC 签名必须有效
3. `X-Nova-Tenant-Key` 必须等于该 Token 的租户
4. 任一不满足 → 拒绝（`401` 或 `403`），**不会**把用量归属到错误租户

非 Nova Token：不加 Nova 头也可正常使用（与现网一致）。

### 5.3 用量何时产生

| 场景 | 是否上报 | source.type / source.key |
|---|---|---|
| 同步 relay 最终结算成功 | 是 | `relay` / New-API request id |
| 通用异步 Task 最终成功且结算完成 | 是 | `task` / `task.TaskID` |
| 预扣、失败、退款、进行中 | 否 | — |
| 旧 Midjourney 独立链路 | 否（本阶段不覆盖） | — |

`quota == 0` 的成功请求默认仍会发布，便于审计。

---

## 6. RabbitMQ 用量消息（Nova 消费端）

### 6.1 本机约定

| 项 | 值 |
|---|---|
| AMQP URL | `amqp://admin:admin123@127.0.0.1:5672/` |
| Management | http://127.0.0.1:15672 （`admin` / `admin123`） |
| Exchange | `nova.events` |
| Type | `topic` |
| Durable | 是 |
| Routing Key | `nova.usage.reported` |
| Delivery Mode | persistent |
| Content-Type | `application/json` |
| Message-Id | 等于 `event_id` |

New-API **不会**替 Nova 声明消费队列；请由测试方自行声明队列、绑定 routing key，并开启消费端幂等。

### 6.2 消息 Envelope（schema v1）

本机实测样例字段形态：

```json
{
  "schema_version": 1,
  "event_id": "6c805f58-7d19-416d-ac60-331ba3d9d5f0",
  "event_type": "nova.usage.reported",
  "occurred_at": "2026-09-18T07:13:32Z",
  "producer": "new-api",
  "tenant_key": "e2e-full-20260918150511",
  "source": {
    "type": "relay",
    "key": "202609180713320941993058268d9d6qf6NFp2t"
  },
  "usage": {
    "quota": 7,
    "model": "deepseek-v3",
    "prompt_tokens": 6,
    "completion_tokens": 1,
    "total_tokens": 7
  },
  "context": {
    "request_id": "202609180713320941993058268d9d6qf6NFp2t",
    "nova_request_id": "nova-usage-1789715612",
    "token_id": 6,
    "token_name": "live",
    "channel_id": 1,
    "group": "default"
  }
}
```

字段规则：

- `usage.quota` **始终存在**且 ≥ 0
- token 数字段为 0 时**省略**（不伪造 0）
- `context` 中空字段省略
- **不含**完整 Token、HMAC secret、Authorization

### 6.3 消费要求（强制）

1. 以 `event_id` 做幂等去重（数据库唯一约束或等价机制）。
2. 同一 `event_id` 可能因 at-least-once 重复投递；重复消息应 ACK 且不重复记账。
3. 也可用 `(source.type, source.key, tenant_key)` 作业务侧辅助核对，权威去重键仍是 `event_id`。
4. 处理成功再 ACK；失败则 NACK/重回队列，避免静默丢账。

---

## 7. 对账与补偿说明

| 机制 | 含义 |
|---|---|
| 事件账本 `nova_usage_events` | 成功用量权威记录；`/logs` 与 MQ 同源 |
| Outbox | 与账本同事务写入；worker 发布到 MQ |
| `usage_candidates`（health） | 结算前归属候选；成功落账后删除；4xx/5xx 会清理 |

**已知限制：**

- 不声明“零丢失自动补偿”。候选可能同时表示“请求仍在途”或“计费成功但事件写入失败”，**禁止盲目自动补发**。
- MQ 故障不会回滚已成功的模型调用与计费；事件会积压在 outbox，恢复后自动投递。
- `dead > 0` 需运维介入（检查 MQ、权限、消息体）。

推荐对账路径：

1. 以 New-API `/api/novapay/.../logs` 或 MQ 消费结果为准核对成功笔数与 `quota`。
2. 差异时查 `event_id` / `source.key` / `nova_request_id`。
3. 积压看 `/api/novapay/health` 的 `outbox` 与 `usage_candidates`。

---

## 8. 联调检查清单

- [ ] HMAC：正确签名可通过；错误签名 / 过期时间 / 重放 nonce / 篡改 body 均 401
- [ ] 创建租户拿到 `sk-` 明文；重放幂等不再返回明文（或使用第 0.3 节现成租户）
- [ ] 额度增减：同 `Idempotency-Key` 重试不重复扣；同 `operation_id` 换新 Key 也不重复扣；同 `operation_id` 不同 delta → `operation_conflict`；新单号才再次生效
- [ ] 软删后 `GET` 仍 200 且 `status=deleted`；再启用/修改返回 409 `tenant_deleted`
- [ ] 管理面累计约 20 次写操作后出现 429，约 20 分钟窗口后恢复
- [ ] 用本机 Token + 归属头成功调用 `POST /v1/chat/completions`（`deepseek-v3`）
- [ ] 错误 `X-Nova-Tenant-Key` 被拒绝
- [ ] 成功请求后 `/logs` 出现记录，且 RabbitMQ 收到同 `event_id` 消息
- [ ] 故意重复投递同一消息，消费端不重复记账
- [ ] `/health` 在 MQ 正常时为 `ok`

---

## 8.1 外部 Agent 实测偏差记录（2026-09-18）

| # | 实测结论 | 文档处理 |
|---|---|---|
| 1 | 旧实现：额度 scope 含 `operation_id`，换 Key 会二次加减 | **已改为** HTTP scope 不含 `operation_id` + `nova_quota_operations` 业务唯一（见 4 / 4.4） |
| 2 | 软删后 `GET /tenant/{key}` 返回 200 + `status=deleted`，不是 404 | 已补充 4.2.1 |
| 3 | 管理面约 20 次 / 20 分钟限流生效，与文档一致 | 无需改动（见 0.6） |

---

## 9. 安全注意

1. 本文件第 0 节密钥与 Token **仅限本机联调**；生产必须轮换并改走 TLS / AMQPS。
2. Secret / Token 明文不得写入业务日志、MQ payload、URL、前端。
3. 密钥轮换：先加 previous，再切 current；移除 previous 前须超过最大时间窗与在途请求时长。
4. 写接口务必使用唯一 `Idempotency-Key`（建议 UUID）。

---

## 10. 联系与变更

- 本机对接文档：仓库根目录 `NOVA_INTEGRATION_GUIDE.md`（本文）
- New-API 内部实现说明：`NOVA_MQ_IMPLEMENTATION_PLAN.md`
- 接口或消息 schema 变更时，应提升 `schema_version` 或提前约定兼容窗口
- 路径前缀保持 `/api/novapay/*` 兼容；如需破坏性变更，双方先评审
- 若本机服务重启后密钥/Token 变更，以运行中容器 `NOVA_HMAC_KEYS` 与数据库 `tokens` 为准，并同步更新第 0 节
