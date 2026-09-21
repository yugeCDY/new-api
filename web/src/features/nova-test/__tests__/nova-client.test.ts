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

import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  canonicalizeQuery,
  consumeRabbitMessages,
  createNovaSignature,
  executeNovaRequest,
  parseNovaEnvironment,
  sha256Hex,
} from '../lib/nova-client'
import type { NovaTestConfig } from '../types'

const config: NovaTestConfig = {
  baseUrl: 'http://127.0.0.1:3000',
  keyId: 'current',
  hmacSecret: 'AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8',
  tenantKey: 'tenant-a',
  apiToken: 'sk-test',
  model: 'deepseek-v3',
  amqpUrl: 'amqp://guest:guest@127.0.0.1:5672/',
  managementUrl: 'http://127.0.0.1:15672',
  mqUsername: '',
  mqPassword: '',
  vhost: '/',
  exchange: 'nova.events',
  routingKey: 'nova.usage.reported',
  queue: 'nova.test.consumer',
  requeue: false,
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('Nova request signing', () => {
  it('imports Bash and PowerShell environment blocks and derives RabbitMQ settings', () => {
    const imported = parseNovaEnvironment(`
      export NOVA_BASE_URL=http://10.0.0.8:3000
      NOVA_HMAC_KEYS=current:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8
      $env:NOVA_TEST_MODEL='deepseek-v3'
      $env:RABBITMQ_URL='amqp://nova:secret@mq.internal:5672/nova-vhost'
    `)

    expect(imported).toMatchObject({
      baseUrl: 'http://10.0.0.8:3000',
      keyId: 'current',
      hmacSecret: 'AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8',
      model: 'deepseek-v3',
      amqpUrl: 'amqp://nova:secret@mq.internal:5672/nova-vhost',
      managementUrl: 'http://mq.internal:15672',
      mqUsername: 'nova',
      mqPassword: 'secret',
      vhost: 'nova-vhost',
    })
  })

  it('sorts query keys and repeated values using Go-compatible encoding', () => {
    expect(canonicalizeQuery('?b=2&a=z&a=a&space=hello%20world&empty=')).toBe(
      'a=a&a=z&b=2&empty=&space=hello+world'
    )
  })

  it('matches the fixed HMAC-SHA256 signing vector', async () => {
    const body = '{"value":0}'
    expect(await sha256Hex(body)).toBe(
      '23d7b286bd429460b92a2a1c21b6afc34110446c5034c17363fda363aa0a7c5d'
    )

    const signed = await createNovaSignature(
      config,
      {
        label: 'test',
        method: 'POST',
        path: '/api/novapay/test',
        query: 'b=2&a=z&a=a',
        body,
      },
      {
        timestamp: '1700000000',
        nonce: 'AQEBAQEBAQEBAQEBAQEBAQEB',
      }
    )

    expect(signed.headers['X-Nova-Signature']).toBe(
      'fb4d9e2ccea274d505eb0c38384de20030d009b69fe7af2008ec311634ad3be2'
    )
    expect(signed.canonical).toContain('\na=a&a=z&b=2\n1700000000\n')
  })

  it('omits X-Nova-Key-Id when the key ID field is empty', async () => {
    const signed = await createNovaSignature(
      { ...config, keyId: '' },
      {
        label: 'Health check',
        method: 'GET',
        path: '/api/novapay/health',
        body: '',
      }
    )

    expect(signed.headers['X-Nova-Key-Id']).toBeUndefined()
    expect(signed.headers['X-Nova-Timestamp']).toBeTruthy()
    expect(signed.headers['X-Nova-Signature']).toMatch(/^[0-9a-f]{64}$/)
  })

  it('sends editable header overrides while redacting authorization in results', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response('{"success":true}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    vi.stubGlobal('fetch', fetchMock)

    const result = await executeNovaRequest(config, {
      label: 'editable request',
      method: 'GET',
      path: '/api/novapay/health',
      headers: {
        authorization: 'Bearer sk-sensitive',
        'X-Nova-Timestamp': '1234567890',
      },
    })

    expect(fetchMock).toHaveBeenCalledWith(
      'http://127.0.0.1:3000/api/novapay/health',
      expect.objectContaining({
        headers: expect.objectContaining({
          authorization: 'Bearer sk-sensitive',
          'X-Nova-Timestamp': '1234567890',
        }),
      })
    )
    expect(result.requestHeaders.authorization).toBe('Bearer ••••••••')
  })
})

describe('RabbitMQ message consumption', () => {
  it('uses the encoded vhost, acknowledges messages, and marks repeated event IDs', async () => {
    const responseBody = JSON.stringify([
      {
        payload: JSON.stringify({
          tenant_key: 'nova-test-3',
          request_id: 'request-1',
          nova_request_id: 'nova-request-1',
          model_type: 'text',
          log: { model_name: 'deepseek-r1', use_time: 0 },
          quota_data: { quota: 42 },
        }),
        routing_key: 'nova.usage.reported',
        properties: { message_id: 'event-1' },
      },
    ])
    const fetchMock = vi.fn().mockImplementation(
      async () =>
        new Response(responseBody, {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
    )
    vi.stubGlobal('fetch', fetchMock)
    const seen = new Set<string>()

    const first = await consumeRabbitMessages(config, seen)
    const second = await consumeRabbitMessages(config, seen)

    expect(fetchMock).toHaveBeenCalledWith(
      'http://127.0.0.1:15672/api/queues/%2F/nova.test.consumer/get',
      expect.objectContaining({
        method: 'POST',
        body: expect.stringContaining('"ackmode":"ack_requeue_false"'),
      })
    )
    expect(first.messages[0]).toMatchObject({
      eventId: 'event-1',
      duplicate: false,
    })
    expect(second.messages[0]).toMatchObject({
      eventId: 'event-1',
      duplicate: true,
    })
  })
})
