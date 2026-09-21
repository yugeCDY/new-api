/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import type {
  NovaRequestResult,
  NovaRequestSpec,
  NovaTestConfig,
  NovaUsageEnvelope,
  RabbitActionResult,
  RabbitMessage,
} from '../types'

const textEncoder = new TextEncoder()
const REQUEST_TIMEOUT_MS = 30_000

const ENV_CONFIG_FIELDS: Record<string, keyof NovaTestConfig> = {
  NOVA_BASE_URL: 'baseUrl',
  NOVA_KEY_ID: 'keyId',
  NOVA_HMAC_SECRET: 'hmacSecret',
  NOVA_TENANT_KEY: 'tenantKey',
  NOVA_TOKEN: 'apiToken',
  NOVA_TEST_MODEL: 'model',
  NOVA_RABBITMQ_URL: 'amqpUrl',
  RABBITMQ_URL: 'amqpUrl',
  RABBITMQ_MGMT: 'managementUrl',
  RABBITMQ_USER: 'mqUsername',
  RABBITMQ_PASSWORD: 'mqPassword',
  RABBITMQ_VHOST: 'vhost',
  NOVA_RABBITMQ_EXCHANGE: 'exchange',
  NOVA_RABBITMQ_ROUTING_KEY: 'routingKey',
  NOVA_TEST_QUEUE: 'queue',
}

function unquoteEnvironmentValue(value: string): string {
  const trimmed = value.trim()
  if (trimmed.length < 2) return trimmed
  const quote = trimmed[0]
  if ((quote === '"' || quote === "'") && trimmed.at(-1) === quote) {
    return trimmed.slice(1, -1)
  }
  return trimmed
}

export function parseNovaEnvironment(source: string): Partial<NovaTestConfig> {
  const parsed: Partial<NovaTestConfig> = {}
  for (const rawLine of source.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue

    const normalized = line.replace(/^export\s+/i, '').replace(/^\$env:/i, '')
    const separator = normalized.indexOf('=')
    if (separator < 1) continue

    const key = normalized.slice(0, separator).trim()
    const value = unquoteEnvironmentValue(normalized.slice(separator + 1))
    if (key === 'NOVA_HMAC_KEYS') {
      const firstPair = value.split(',')[0] ?? ''
      const colon = firstPair.indexOf(':')
      if (colon > 0) {
        parsed.keyId = firstPair.slice(0, colon).trim()
        parsed.hmacSecret = firstPair.slice(colon + 1).trim()
      }
      continue
    }

    const field = ENV_CONFIG_FIELDS[key]
    if (field && field !== 'requeue') parsed[field] = value
  }

  if (parsed.amqpUrl) {
    try {
      const amqp = new URL(parsed.amqpUrl)
      parsed.mqUsername ||= decodeURIComponent(amqp.username)
      parsed.mqPassword ||= decodeURIComponent(amqp.password)
      if (amqp.pathname.length > 1) {
        parsed.vhost ||= decodeURIComponent(amqp.pathname.slice(1))
      }
      if (!parsed.managementUrl) {
        const protocol = amqp.protocol === 'amqps:' ? 'https:' : 'http:'
        parsed.managementUrl = `${protocol}//${amqp.hostname}:15672`
      }
    } catch {
      // Form validation reports malformed URLs after import.
    }
  }
  return parsed
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary)
}

function bytesToHex(bytes: ArrayBuffer): string {
  return [...new Uint8Array(bytes)]
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('')
}

function decodeBase64Url(value: string): Uint8Array {
  const normalized = value.trim().replaceAll('-', '+').replaceAll('_', '/')
  const paddingLength = (4 - (normalized.length % 4)) % 4
  const decoded = atob(normalized + '='.repeat(paddingLength))
  return Uint8Array.from(decoded, (char) => char.charCodeAt(0))
}

function encodeQueryComponent(value: string): string {
  return encodeURIComponent(value)
    .replaceAll('%20', '+')
    .replaceAll(
      /[!'()*]/g,
      (char) => `%${char.charCodeAt(0).toString(16).toUpperCase()}`
    )
}

export function canonicalizeQuery(query: string): string {
  const source = query.startsWith('?') ? query.slice(1) : query
  if (!source) return ''

  const entries = [...new URLSearchParams(source).entries()]
  entries.sort(([leftKey, leftValue], [rightKey, rightValue]) => {
    if (leftKey !== rightKey) return leftKey < rightKey ? -1 : 1
    if (leftValue === rightValue) return 0
    return leftValue < rightValue ? -1 : 1
  })
  return entries
    .map(
      ([key, value]) =>
        `${encodeQueryComponent(key)}=${encodeQueryComponent(value)}`
    )
    .join('&')
}

export async function sha256Hex(value: string): Promise<string> {
  const digest = await globalThis.crypto.subtle.digest(
    'SHA-256',
    textEncoder.encode(value)
  )
  return bytesToHex(digest)
}

function createNonce(): string {
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(18))
  return bytesToBase64(bytes)
    .replaceAll('+', '-')
    .replaceAll('/', '_')
    .replaceAll('=', '')
}

export async function createNovaSignature(
  config: Pick<NovaTestConfig, 'keyId' | 'hmacSecret'>,
  spec: NovaRequestSpec,
  signatureContext?: { timestamp: string; nonce: string }
): Promise<{
  canonical: string
  headers: Record<string, string>
}> {
  const secret = decodeBase64Url(config.hmacSecret)
  if (secret.byteLength < 32) {
    throw new Error('HMAC secret must decode to at least 32 bytes')
  }

  const timestamp =
    signatureContext?.timestamp ?? Math.floor(Date.now() / 1000).toString()
  const nonce = signatureContext?.nonce ?? createNonce()
  const bodyHash = await sha256Hex(spec.body ?? '')
  const canonical = [
    spec.method.toUpperCase(),
    spec.path || '/',
    canonicalizeQuery(spec.query ?? ''),
    timestamp,
    nonce,
    bodyHash,
  ].join('\n')
  const key = await globalThis.crypto.subtle.importKey(
    'raw',
    new Uint8Array(secret).buffer,
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign']
  )
  const signature = await globalThis.crypto.subtle.sign(
    'HMAC',
    key,
    textEncoder.encode(canonical)
  )

  return {
    canonical,
    headers: {
      ...(config.keyId.trim()
        ? { 'X-Nova-Key-Id': config.keyId.trim() }
        : {}),
      'X-Nova-Timestamp': timestamp,
      'X-Nova-Nonce': nonce,
      'X-Nova-Signature': bytesToHex(signature),
    },
  }
}

function joinTargetUrl(baseUrl: string, path: string, query?: string): string {
  const normalizedBase = baseUrl.trim().replace(/\/$/, '')
  const normalizedPath = path.startsWith('/') ? path : `/${path}`
  const normalizedQuery = query?.replace(/^\?/, '')
  return `${normalizedBase}${normalizedPath}${normalizedQuery ? `?${normalizedQuery}` : ''}`
}

function readHeaders(headers: Headers): Record<string, string> {
  return Object.fromEntries(headers.entries())
}

function hasHeader(headers: Record<string, string>, name: string): boolean {
  const normalizedName = name.toLowerCase()
  return Object.keys(headers).some(
    (headerName) => headerName.toLowerCase() === normalizedName
  )
}

function redactRequestHeaders(
  headers: Record<string, string>
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(headers).map(([key, value]) => [
      key,
      key.toLowerCase() === 'authorization' ? 'Bearer ••••••••' : value,
    ])
  )
}

export async function executeNovaRequest(
  config: NovaTestConfig,
  spec: NovaRequestSpec
): Promise<NovaRequestResult> {
  const startedAt = performance.now()
  const timestamp = new Date().toISOString()
  const url = joinTargetUrl(config.baseUrl, spec.path, spec.query)
  let canonical = ''
  let requestHeaders: Record<string, string> = {}

  try {
    const target = new URL(url)
    const signed = await createNovaSignature(config, {
      ...spec,
      path: target.pathname,
      query: target.search.slice(1),
    })
    canonical = signed.canonical
    requestHeaders = {
      Accept: 'application/json',
      ...signed.headers,
      ...spec.headers,
    }
    if (spec.body && !hasHeader(requestHeaders, 'Content-Type')) {
      requestHeaders['Content-Type'] = 'application/json'
    }
    if (spec.bearerToken) {
      requestHeaders.Authorization = `Bearer ${spec.bearerToken}`
    }
    if (spec.tenantKey) {
      requestHeaders['X-Nova-Tenant-Key'] = spec.tenantKey
    }
    if (spec.novaRequestId) {
      requestHeaders['X-Nova-Request-Id'] = spec.novaRequestId
    }

    const controller = new AbortController()
    const timeout = globalThis.setTimeout(
      () => controller.abort(),
      REQUEST_TIMEOUT_MS
    )
    let response: Response
    try {
      response = await fetch(url, {
        method: spec.method,
        headers: requestHeaders,
        body: spec.body || undefined,
        credentials: 'omit',
        cache: 'no-store',
        signal: controller.signal,
      })
    } finally {
      globalThis.clearTimeout(timeout)
    }
    const responseBody = await response.text()
    return {
      id: globalThis.crypto.randomUUID(),
      label: spec.label,
      method: spec.method.toUpperCase(),
      url,
      ok: response.ok,
      status: response.status,
      durationMs: Math.round(performance.now() - startedAt),
      timestamp,
      canonical,
      requestHeaders: redactRequestHeaders(requestHeaders),
      requestBody: spec.body ?? '',
      responseHeaders: readHeaders(response.headers),
      responseBody,
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    return {
      id: globalThis.crypto.randomUUID(),
      label: spec.label,
      method: spec.method.toUpperCase(),
      url,
      ok: false,
      status: null,
      durationMs: Math.round(performance.now() - startedAt),
      timestamp,
      canonical,
      requestHeaders: redactRequestHeaders(requestHeaders),
      requestBody: spec.body ?? '',
      responseHeaders: {},
      responseBody: '',
      error: message,
    }
  }
}

function rabbitCredentials(config: NovaTestConfig): {
  username: string
  password: string
  vhost: string
} {
  let username = config.mqUsername
  let password = config.mqPassword
  let vhost = config.vhost
  try {
    const url = new URL(config.amqpUrl)
    username ||= decodeURIComponent(url.username)
    password ||= decodeURIComponent(url.password)
    if (!vhost && url.pathname.length > 1) {
      vhost = decodeURIComponent(url.pathname.slice(1))
    }
  } catch {
    // Explicit Management API fields remain usable without a valid AMQP URL.
  }
  return { username, password, vhost: vhost || '/' }
}

async function rabbitRequest(
  config: NovaTestConfig,
  path: string,
  method: string,
  body?: Record<string, unknown>
): Promise<RabbitActionResult> {
  const credentials = rabbitCredentials(config)
  const basic = bytesToBase64(
    textEncoder.encode(`${credentials.username}:${credentials.password}`)
  )
  const target = `${config.managementUrl.trim().replace(/\/$/, '')}${path}`
  try {
    const controller = new AbortController()
    const timeout = globalThis.setTimeout(
      () => controller.abort(),
      REQUEST_TIMEOUT_MS
    )
    let response: Response
    try {
      response = await fetch(target, {
        method,
        headers: {
          Accept: 'application/json',
          Authorization: `Basic ${basic}`,
          ...(body ? { 'Content-Type': 'application/json' } : {}),
        },
        body: body ? JSON.stringify(body) : undefined,
        credentials: 'omit',
        cache: 'no-store',
        signal: controller.signal,
      })
    } finally {
      globalThis.clearTimeout(timeout)
    }
    return {
      ok: response.ok,
      status: response.status,
      body: await response.text(),
    }
  } catch (error) {
    return {
      ok: false,
      status: null,
      body: '',
      error: error instanceof Error ? error.message : String(error),
    }
  }
}

function rabbitPath(config: NovaTestConfig): {
  vhost: string
  queue: string
  exchange: string
} {
  const credentials = rabbitCredentials(config)
  return {
    vhost: encodeURIComponent(credentials.vhost),
    queue: encodeURIComponent(config.queue),
    exchange: encodeURIComponent(config.exchange),
  }
}

export function declareRabbitQueue(
  config: NovaTestConfig
): Promise<RabbitActionResult> {
  const path = rabbitPath(config)
  return rabbitRequest(
    config,
    `/api/queues/${path.vhost}/${path.queue}`,
    'PUT',
    {
      durable: true,
      auto_delete: false,
      arguments: {},
    }
  )
}

export function bindRabbitQueue(
  config: NovaTestConfig
): Promise<RabbitActionResult> {
  const path = rabbitPath(config)
  return rabbitRequest(
    config,
    `/api/bindings/${path.vhost}/e/${path.exchange}/q/${path.queue}`,
    'POST',
    { routing_key: config.routingKey, arguments: {} }
  )
}

export function deleteRabbitQueue(
  config: NovaTestConfig
): Promise<RabbitActionResult> {
  const path = rabbitPath(config)
  return rabbitRequest(
    config,
    `/api/queues/${path.vhost}/${path.queue}`,
    'DELETE'
  )
}

type RabbitManagementMessage = {
  payload?: string
  payload_encoding?: string
  redelivered?: boolean
  routing_key?: string
  properties?: { message_id?: string }
}

export async function consumeRabbitMessages(
  config: NovaTestConfig,
  seenEventIds: Set<string>
): Promise<{ result: RabbitActionResult; messages: RabbitMessage[] }> {
  const path = rabbitPath(config)
  const result = await rabbitRequest(
    config,
    `/api/queues/${path.vhost}/${path.queue}/get`,
    'POST',
    {
      count: 20,
      ackmode: config.requeue ? 'ack_requeue_true' : 'ack_requeue_false',
      encoding: 'auto',
      truncate: 100_000,
    }
  )
  if (!result.ok) return { result, messages: [] }

  let rawMessages: RabbitManagementMessage[]
  try {
    const parsed = JSON.parse(result.body) as unknown
    rawMessages = Array.isArray(parsed) ? parsed : []
  } catch {
    return {
      result: { ...result, ok: false, error: 'RabbitMQ returned invalid JSON' },
      messages: [],
    }
  }

  const receivedAt = new Date().toISOString()
  const messages = rawMessages.map((message) => {
    const payload = message.payload ?? ''
    let parsed: NovaUsageEnvelope | null = null
    try {
      const value = JSON.parse(payload) as unknown
      if (value && typeof value === 'object' && !Array.isArray(value)) {
        parsed = value as NovaUsageEnvelope
      }
    } catch {
      parsed = null
    }
    const eventId =
      message.properties?.message_id ||
      parsed?.event_id ||
      '(missing event_id)'
    const duplicate = seenEventIds.has(eventId)
    seenEventIds.add(eventId)
    return {
      id: globalThis.crypto.randomUUID(),
      eventId,
      duplicate,
      redelivered: Boolean(message.redelivered),
      routingKey: message.routing_key ?? '',
      payload,
      parsed,
      receivedAt,
    }
  })
  return { result, messages }
}
