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

export type NovaTestConfig = {
  baseUrl: string
  keyId: string
  hmacSecret: string
  tenantKey: string
  apiToken: string
  model: string
  amqpUrl: string
  managementUrl: string
  mqUsername: string
  mqPassword: string
  vhost: string
  exchange: string
  routingKey: string
  queue: string
  requeue: boolean
}

export type NovaRequestSpec = {
  label: string
  method: string
  path: string
  query?: string
  body?: string
  bearerToken?: string
  tenantKey?: string
  novaRequestId?: string
  headers?: Record<string, string>
}

export type NovaRequestResult = {
  id: string
  label: string
  method: string
  url: string
  ok: boolean
  status: number | null
  durationMs: number
  timestamp: string
  canonical: string
  requestHeaders: Record<string, string>
  requestBody: string
  responseHeaders: Record<string, string>
  responseBody: string
  error?: string
}

export type NovaUsageEnvelope = {
  schema_version?: number
  event_id?: string
  event_type?: string
  occurred_at?: string
  producer?: string
  tenant_key?: string
  source?: {
    type?: string
    key?: string
  }
  usage?: {
    quota?: number
    model?: string
    prompt_tokens?: number
    completion_tokens?: number
    total_tokens?: number
  }
  context?: Record<string, unknown>
  [key: string]: unknown
}

export type RabbitMessage = {
  id: string
  eventId: string
  duplicate: boolean
  redelivered: boolean
  routingKey: string
  payload: string
  parsed: NovaUsageEnvelope | null
  receivedAt: string
}

export type RabbitActionResult = {
  ok: boolean
  status: number | null
  body: string
  error?: string
}

export type NovaSuiteStepStatus = 'pending' | 'running' | 'passed' | 'failed'

export type NovaSuiteStep = {
  id: string
  status: NovaSuiteStepStatus
  detail?: string
}
