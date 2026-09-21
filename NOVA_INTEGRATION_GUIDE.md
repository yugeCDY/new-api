# New-API ↔ Nova 对接指南

面向 **Nova 平台开发人员 / 联调 Agent** 的对接说明。覆盖管理 API、HMAC 认证、业务请求归属头、RabbitMQ 用量消息与健康检查。

| 项 | 值 |
|---|---|
| 文档版本 | 1.4（额度调整：`order_no` + `delta_quota`/`absolute_quota`） |
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

请求头（`X-Nova-Key-Id` 可省略，省略则用 current）：

```http
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
   `GET http://127.0.0.1:3000/api/novapay/health` + 正确签名 → `200` 且 `data.status=up`，`data.database.connected=true`，`data.mq.connected=true`。  
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
| `X-Nova-Key-Id` | **可选**。省略时使用 `NOVA_HMAC_KEYS` 里的第一把（current）。单密钥部署可不传；密钥轮换时再显式指定 |
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
    # X-Nova-Key-Id is optional for single-key deployments.
    return {
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
- **写操作**必须在 body 带 `request_id`：`^[A-Za-z0-9._:-]{8,128}$`（无 body 的写接口也要传 `{"request_id":"..."}`）
- **不再使用**请求头 `Idempotency-Key`
- HTTP 幂等维度是 **`(scope, request_id)`**：
  - **同一 scope + 同一 `request_id` + 同一 body hash** → 返回原结果，响应头 `Idempotency-Replayed: true`
  - **同一 scope + 同一 `request_id` + 不同 body hash** → `409` / `idempotency_conflict`
  - 租户额度 scope：`tenant_quota:{tenant_key}`（业务防重另用 `order_no`）；令牌额度 scope：`token_quota:{tenant_key}:{token_name}`（仅 `request_id`）
- **额度业务防重**另有一层：表 `nova_quota_operations` 对 `(tenant_key, target_type, target_ref, order_no)` 唯一（库列名仍为 `operation_id`）
  - 同一 `order_no` + 相同调额参数/`reason`（即使换了新的 `request_id`）→ 不重复加减，body 带 `"replayed": true`
  - 同一 `order_no` + 不同调额参数/`reason` → `409` / `operation_conflict`
- 分页：`page`（默认 1）、`page_size`（默认 20，最大 100）
- 额度单位：New-API **quota 整数**（非美元）；范围 `0 .. 2^53-1`
- **统一响应格式**：
  - 成功：`{ "success": true, "message": "ok", "data": ... }`
  - 租户列表的 `total` / `page` / `page_size` / `items` 在 `data` 内；用量账本的分页字段仍在顶层
  - 失败：`{ "success": false, "code": "...", "message": "<错误信息>", "request_id": "..." }`

### 4.1 路由一览

| 方法 | 路径 | 幂等（body `request_id`） | 用途 |
|---|---|---|---|
| `POST` | `/api/novapay/tenant` | 是 | 创建租户 + 专属用户 + 初始 Token |
| `GET` | `/api/novapay/tenant/{tenant_key}` | 否 | 查询租户 |
| `PUT` | `/api/novapay/tenant/{tenant_key}` | 是 | 更新显示名 |
| `DELETE` | `/api/novapay/tenant/{tenant_key}` | 是 | 软删除（不可再启用） |
| `GET` | `/api/novapay/tenants` | 否 | 分页列表；可按 `keyword`、`status` 过滤 |
| `POST` | `/api/novapay/tenant/{tenant_key}/quota` | 是 | 调整租户用户余额 |
| `POST` | `/api/novapay/tenant/{tenant_key}/disable` | 是 | 禁用租户及全部 Token |
| `POST` | `/api/novapay/tenant/{tenant_key}/enable` | 是 | 启用租户（**不**自动启用曾单独禁用的 Token） |
| `POST` | `/api/novapay/tenant/{tenant_key}/token/rotate` | 是 | 密钥轮换；明文在首次与同 `request_id` 重放时返回 |
| `GET` | `/api/novapay/tenant/{tenant_key}/tokens` | 否 | 租户密钥查询（含完整 key） |
| `POST` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}/quota` | 是 | 令牌级额度设置（`remain_quota` / `unlimited_quota`） |
| `DELETE` | `/api/novapay/tenant/{tenant_key}/tokens/{token_name}` | 是 | 吊销 Token |
| `GET` | `/api/novapay/tenant/{tenant_key}/logs` | 否 | 成功用量账本 |
| `GET` | `/api/novapay/models` | 否 | 可用模型目录（渠道数、计费、端点） |
| `GET` | `/api/novapay/health` | 否 | 主库 / Redis / MQ / outbox 积压 |

本机可先测（只读，少触发限流）：

- `GET http://127.0.0.1:3000/api/novapay/health`
- `GET http://127.0.0.1:3000/api/novapay/tenant/e2e-full-20260918150511`
- `GET http://127.0.0.1:3000/api/novapay/tenant/e2e-full-20260918150511/logs?page=1&page_size=20`
- `GET http://127.0.0.1:3000/api/novapay/models`

### 4.2 标识规则

| 字段 | 规则 |
|---|---|
| `tenant_key` | `^[A-Za-z0-9][A-Za-z0-9._-]{2,19}$`（3–20），写入 `users.username`，创建后不可变 |
| `tenant_name` / `display_name` | 最长 20（写入 `users.display_name`） |
| `token_name` | `^[A-Za-z0-9][A-Za-z0-9._-]{0,49}$` |
| `order_no`（额度调整） | 1–128，`[A-Za-z0-9._:-]` |
| `request_id`（全部写接口） | 8–128，`[A-Za-z0-9._:-]` |

### 4.2.1 软删除语义（已实测）

- `DELETE /api/novapay/tenant/{tenant_key}` 为**软删除**：对应用户 `users.deleted_at` 置位，账本与历史保留。
- 之后 `GET /api/novapay/tenant/{tenant_key}` 仍返回 **HTTP 200**，body 中 `data.status=deleted`（**不是 404**）。
- 客户端应以 `data.status` 字段判断是否可用。
- 对已删除租户再 `enable` / `update` / 调额等写操作 → **409** / `tenant_deleted`。

路径参数 `{tenant_key}` 即 `users.username`。租户状态来自用户表：`enabled` / `disabled` 对应 `users.status`，`deleted` 对应用户软删。

### 4.2.2 租户列表

`GET /api/novapay/tenants?page=1&page_size=20&keyword=tmts&status=enabled`

`keyword` 匹配 `username`、`display_name`（大小写不敏感）。`status` 只能是 `enabled` / `disabled` / `deleted`。不返回 token 明文或 `token_key`。

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "total": 37,
    "page": 1,
    "page_size": 20,
    "items": [
      {
        "username": "tmts-a",
        "user_id": 18,
        "display_name": "tmts",
        "status": "enabled",
        "quota": 4820000,
        "used_quota": 180000,
        "created_at": 1710000000,
        "last_active_at": 1710003600
      }
    ]
  }
}
```

`created_at` / `last_active_at` 为 Unix 秒。`last_active_at` 取最近一次成功用量与用户最后登录时间中较晚的一个；用户记录已删除时 `username` 为空、额度按 0。

### 4.2.3 查询租户详情

`GET http://127.0.0.1:3000/api/novapay/tenant/{tenant_key}`

返回租户详情；`token_key` 为掩码（完整明文仅创建/轮换时返回）。`status`：`enabled` / `disabled` / `deleted`。`created_at` / `last_active_at` 为 RFC3339 UTC。

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "user_id": 18,
    "username": "tmts44j8p8g2r",
    "display_name": "示例科技有限公司",
    "status": "enabled",
    "quota": 4820000,
    "used_quota": 180000,
    "token_name": "nova-tmts44j8p8g2r",
    "token_key": "sk-xxxx**********xxxx",
    "created_at": "2026-09-13T08:00:00Z",
    "last_active_at": "2026-09-13T09:30:00Z"
  }
}
```

### 4.3 创建租户

`POST http://127.0.0.1:3000/api/novapay/tenant`

不需要 `Idempotency-Key` 头；**所有写接口**幂等统一靠 body 里的 `request_id`。

```json
{
  "tenant_key": "tmts44j8p8g2r",
  "tenant_name": "示例科技有限公司",
  "initial_quota": 5000000,
  "request_id": "req-20260913-0001"
}
```

| 字段 | 说明 |
|---|---|
| `tenant_key` | 必填；平台租户唯一键 |
| `tenant_name` | 必填；展示名称，最长 20（写入 `users.display_name`） |
| `initial_quota` | 必填；初始额度（用户余额与初始 Token 额度） |
| `request_id` | 必填；请求唯一号，规则 `^[A-Za-z0-9._:-]{8,128}$` |

成功响应：

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "user_id": 18,
    "username": "tmts44j8p8g2r",
    "token_name": "nova-tmts44j8p8g2r",
    "token_key": "sk-xxxxxxxx",
    "quota": 5000000
  }
}
```

- `username` 等于创建时传入的 `tenant_key`（存于 `users.username`）
- `token_name` 为 `nova-` + `tenant_key`（超长会截断到 50）
- 相同 `request_id` + 相同 body 重放时返回同一 `token_key`，并带 `Idempotency-Replayed: true`
- 相同 `request_id` + 不同 body → `409` / `idempotency_conflict`
- 若不想新建租户，直接使用第 0.3 节已有租户与 Token

### 4.4 调整额度

`POST http://127.0.0.1:3000/api/novapay/tenant/{tenant_key}/quota`

增量模式：

```json
{
  "request_id": "req-20260913-0002",
  "order_no": "R2026091300001",
  "delta_quota": 1000000,
  "reason": "recharge"
}
```

绝对值模式（二选一，不可同时传）：

```json
{
  "request_id": "req-20260913-0003",
  "order_no": "R2026091300002",
  "absolute_quota": 6000000,
  "reason": "reconcile"
}
```

成功响应示例：

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "username": "tenant-a",
    "quota_before": 4820000,
    "quota_after": 5820000,
    "delta_quota": 1000000,
    "order_no": "R2026091300001",
    "reason": "recharge",
    "replayed": false
  }
}
```

- `delta_quota` 可为负（扣减）；`absolute_quota` 直接设定目标余额。二者必须且只能传一个。
- 不会把余额扣到允许范围以下，也不会超过钱包上限。
- **两层防重**：
  | 层 | 作用 | 行为 |
  |---|---|---|
  | `request_id` | HTTP 超时重试 | 同 `request_id` 同 body → 原响应 + `Idempotency-Replayed: true`；同 `request_id` 不同 body → `409 idempotency_conflict` |
  | `order_no` | 业务单号（库唯一） | 同 `order_no` 同 payload → 不重复加减，`"replayed": true`；同 `order_no` 不同 payload → `409 operation_conflict` |
  - 网络重试：复用同一 `request_id` 即可。
  - 业务重试 / 换 `request_id` 再发：只要 `order_no` 与 payload 不变，也不会二次加减。
  - 新的一笔调额：必须使用**新的** `order_no`（并建议新的 `request_id`）。

### 4.5 密钥轮换

`POST http://127.0.0.1:3000/api/novapay/tenant/{tenant_key}/token/rotate`

```json
{
  "reason": "疑似泄露",
  "request_id": "req-20260913-0004"
}
```

成功响应示例：

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "old_token_name": "nova-tenant-a",
    "token_name": "nova-tenant-a",
    "token_key": "sk-xxxxxxxx"
  }
}
```

- 就地轮换租户主令牌（默认名 `nova-{tenant_key}`）的密钥；令牌名与额度不变。
- 旧 `sk-...` **立即失效**；新明文仅在首次响应与同 `request_id` 重放时返回（重放带 `Idempotency-Replayed: true`）。
- `reason` 必填（1–255），用于说明轮换原因。

### 4.5.2 令牌级额度设置

`POST http://127.0.0.1:3000/api/novapay/tenant/{tenant_key}/tokens/{token_name}/quota`

```json
{
  "remain_quota": 500000,
  "unlimited_quota": false,
  "reason": "员工额度调整",
  "request_id": "req-20260913-token-quota-1"
}
```

成功响应示例：

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "token_name": "nova-tenant-a",
    "remain_quota": 500000,
    "unlimited_quota": false
  }
}
```

- 直接设置该 Token 的 `remain_quota` / `unlimited_quota`（New-API 原生字段；`unlimited_quota=false` 时按 Token 扣费）。
- 幂等仅依赖 `request_id`（无 `order_no`）。
- 若 Token 因额度耗尽为 `exhausted`，设置后额度可用时会恢复为 `enabled`。

### 4.5.1 租户密钥查询

`GET http://127.0.0.1:3000/api/novapay/tenant/{tenant_key}/tokens`

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "items": [
      {
        "token_id": 7,
        "token_name": "nova-tenant-a",
        "key": "sk-xxxxxxxx",
        "status": "enabled",
        "expired_time": -1,
        "remain_quota": 4820000,
        "unlimited_quota": false,
        "used_quota": 180000,
        "created_time": 1726200000
      }
    ]
  }
}
```

- 返回该租户下全部 API 密钥，**含完整 `key` 明文**（仅管理面 HMAC 可访问）。
- `status`：`enabled` / `disabled` / `expired` / `exhausted`。
- `expired_time` 为 Unix 秒；`-1` 表示永不过期。`created_time` 同为 Unix 秒。

### 4.6 用量日志

`GET http://127.0.0.1:3000/api/novapay/tenant/e2e-full-20260918150511/logs?page=1&page_size=20`

返回 Nova **事件账本**（非 `logs` / `quota_data` 表），字段包括：

`event_id`、`source_type`（`relay` / `task`）、`source_key`、`tenant_key`、`quota`、`model_name`、`prompt_tokens`、`completion_tokens`、`total_tokens`、`request_id`、`nova_request_id`、`occurred_at` 等。

### 4.6.1 模型目录

`GET http://127.0.0.1:3000/api/novapay/models`

只返回当前可路由的模型（启用中的渠道能力）。不返回渠道密钥、上游地址或渠道名称。

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "items": [
      {
        "model_name": "kwjm-openai/deepseek-v4-flash",
        "channel_count": 2,
        "enabled": true,
        "quota_type": 0,
        "model_ratio": 1,
        "completion_ratio": 1,
        "enable_groups": ["default"],
        "supported_endpoint_types": ["openai"]
      }
    ]
  }
}
```

| 字段 | 说明 |
|---|---|
| `model_name` | 调用时使用的模型名 |
| `channel_count` | 当前启用渠道数（同一渠道多分组只计 1） |
| `enabled` | 至少有 1 个启用渠道 |
| `description` / `tags` / `vendor_name` | 有元数据时才出现 |
| `quota_type` | `0` 按 token 倍率，`1` 按次计价 |
| `model_ratio` / `completion_ratio` | `quota_type=0` 时出现 |
| `model_price` | `quota_type=1` 时出现，单位与网关定价一致 |
| `cache_ratio` 等 | 仅该模型配置了对应倍率时出现 |
| `enable_groups` | 可使用该模型的分组 |
| `supported_endpoint_types` | 可调用的协议端点，如 `openai` |

### 4.7 健康检查

`GET http://127.0.0.1:3000/api/novapay/health`

期望示例：

```json
{
  "success": true,
  "message": "ok",
  "data": {
    "status": "up",
    "database": {
      "connected": true
    },
    "redis": {
      "enabled": true,
      "connected": true
    },
    "mq": {
      "connected": true,
      "outbox_pending": 0,
      "outbox_dead": 0
    }
  }
}
```

探测范围（Nova 相关中间件）：

| 组件 | 是否必选 | 说明 |
|---|---|---|
| `database` | 必选 | 主库；租户、幂等、nonce、账本、outbox 都在这里 |
| `mq` | 必选 | RabbitMQ；用量事件发布；`outbox_*` 为积压指标 |
| `redis` | 可选 | 宿主网关缓存/额度路径；未配置 `REDIS_CONN_STRING` 时 `enabled=false`，不影响 `status` |

不纳入本接口：日志库 / ClickHouse（Nova 账本不走它们）、RabbitMQ Management HTTP（仅运维 UI）。

`status`：`up` / `degraded` / `down`。

- `down`：主库不可用
- `degraded`：主库可用，但 MQ 未连通，或 Redis 已启用却不可达
- `up`：上述必选依赖正常（Redis 未启用时忽略）

建议告警：

| 条件 | 建议 |
|---|---|
| `data.database.connected == false` | 立即告警 |
| `data.mq.connected == false` | 立即告警 |
| `data.redis.enabled == true && data.redis.connected == false` | 告警 |
| `data.mq.outbox_pending > 1000` | 告警 |
| `data.mq.outbox_dead > 0` | 立即告警 |
| `data.status` 为 `degraded` / `down` | 告警 |

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
| `nova_attributions`（usage candidates） | 结算前归属候选；成功落账后删除；4xx/5xx 会清理；不再出现在 health 响应里 |

**已知限制：**

- 不声明“零丢失自动补偿”。候选可能同时表示“请求仍在途”或“计费成功但事件写入失败”，**禁止盲目自动补发**。
- MQ 故障不会回滚已成功的模型调用与计费；事件会积压在 outbox，恢复后自动投递。
- `dead > 0` 需运维介入（检查 MQ、权限、消息体）。

推荐对账路径：

1. 以 New-API `/api/novapay/.../logs` 或 MQ 消费结果为准核对成功笔数与 `quota`。
2. 差异时查 `event_id` / `source.key` / `nova_request_id`。
3. 积压看 `/api/novapay/health` 的 `data.mq.outbox_pending` / `data.mq.outbox_dead`。

---

## 8. 联调检查清单

- [ ] HMAC：正确签名可通过；错误签名 / 过期时间 / 重放 nonce / 篡改 body 均 401
- [ ] 创建租户拿到 `token_key`；相同 `request_id` 重放返回同一 `token_key`（或使用第 0.3 节现成租户）
- [ ] 额度增减：同 `request_id` 重试不重复扣；同 `order_no` 换新 `request_id` 也不重复扣；同 `order_no` 不同 delta/absolute → `operation_conflict`；新单号才再次生效
- [ ] 软删后 `GET` 仍 200 且 `status=deleted`；再启用/修改返回 409 `tenant_deleted`
- [ ] 管理面累计约 20 次写操作后出现 429，约 20 分钟窗口后恢复
- [ ] 用本机 Token + 归属头成功调用 `POST /v1/chat/completions`（`deepseek-v3`）
- [ ] 错误 `X-Nova-Tenant-Key` 被拒绝
- [ ] 成功请求后 `/logs` 出现记录，且 RabbitMQ 收到同 `event_id` 消息
- [ ] 故意重复投递同一消息，消费端不重复记账
- [ ] `/health` 在主库与 MQ 正常时为 `data.status=up`

---

## 8.1 外部 Agent 实测偏差记录（2026-09-18）

| # | 实测结论 | 文档处理 |
|---|---|---|
| 1 | 旧实现：额度 scope 含业务单号，换 Key 会二次加减 | **已改为** HTTP scope 不含 `order_no` + `nova_quota_operations` 业务唯一（见 4 / 4.4） |
| 2 | 软删后 `GET /tenant/{key}` 返回 200 + `status=deleted`，不是 404 | 已补充 4.2.1 |
| 3 | 管理面约 20 次 / 20 分钟限流生效，与文档一致 | 无需改动（见 0.6） |

---

## 9. 安全注意

1. 本文件第 0 节密钥与 Token **仅限本机联调**；生产必须轮换并改走 TLS / AMQPS。
2. Secret / Token 明文不得写入业务日志、MQ payload、URL、前端。
3. 密钥轮换：先加 previous，再切 current；移除 previous 前须超过最大时间窗与在途请求时长。
4. 写接口务必使用唯一 `request_id`（建议 UUID）。

---

## 10. 联系与变更

- 本机对接文档：仓库根目录 `NOVA_INTEGRATION_GUIDE.md`（本文）
- New-API 内部实现说明：`NOVA_MQ_IMPLEMENTATION_PLAN.md`
- 接口或消息 schema 变更时，应提升 `schema_version` 或提前约定兼容窗口
- 路径前缀保持 `/api/novapay/*` 兼容；如需破坏性变更，双方先评审
- 若本机服务重启后密钥/Token 变更，以运行中容器 `NOVA_HMAC_KEYS` 与数据库 `tokens` 为准，并同步更新第 0 节
