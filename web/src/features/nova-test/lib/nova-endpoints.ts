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

export type NovaEndpointTemplate = {
  id: string
  labelKey: string
  descriptionKey: string
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  path: string
  headers: string
  body: string
  category: 'management' | 'model'
}

const JSON_HEADERS = JSON.stringify({ Accept: 'application/json' }, null, 2)
const WRITE_HEADERS = JSON.stringify(
  {
    Accept: 'application/json',
    'Content-Type': 'application/json',
  },
  null,
  2
)

export const NOVA_ENDPOINTS: NovaEndpointTemplate[] = [
  {
    id: 'health',
    labelKey: 'Health check',
    descriptionKey:
      'Checks the Nova module, database, outbox, and RabbitMQ status.',
    method: 'GET',
    path: '/api/novapay/health',
    headers: JSON_HEADERS,
    body: '',
    category: 'management',
  },
  {
    id: 'models',
    labelKey: 'Model catalog',
    descriptionKey:
      'Reads models and Nova pricing information available to tenants.',
    method: 'GET',
    path: '/api/novapay/models',
    headers: JSON_HEADERS,
    body: '',
    category: 'management',
  },
  {
    id: 'tenants',
    labelKey: 'List tenants',
    descriptionKey:
      'Lists Nova tenants with pagination and an optional status filter.',
    method: 'GET',
    path: '/api/novapay/tenants?page=1&page_size=20',
    headers: JSON_HEADERS,
    body: '',
    category: 'management',
  },
  {
    id: 'create-tenant',
    labelKey: 'Create tenant',
    descriptionKey:
      'Creates a tenant account and returns token_key. Idempotent via request_id.',
    method: 'POST',
    path: '/api/novapay/tenant',
    headers: WRITE_HEADERS,
    body: JSON.stringify(
      {
        tenant_key: '{{tenant_key}}',
        tenant_name: 'Nova Test Tenant',
        initial_quota: 1000000,
        request_id: '{{request_id}}',
      },
      null,
      2
    ),
    category: 'management',
  },
  {
    id: 'get-tenant',
    labelKey: 'Get tenant',
    descriptionKey: 'Reads the current tenant status and remaining quota.',
    method: 'GET',
    path: '/api/novapay/tenant/{{tenant_key}}',
    headers: JSON_HEADERS,
    body: '',
    category: 'management',
  },
  {
    id: 'update-tenant',
    labelKey: 'Update tenant',
    descriptionKey: 'Updates the tenant display name.',
    method: 'PUT',
    path: '/api/novapay/tenant/{{tenant_key}}',
    headers: WRITE_HEADERS,
    body: JSON.stringify(
      {
        request_id: '{{request_id}}',
        display_name: 'Nova Test Tenant Updated',
      },
      null,
      2
    ),
    category: 'management',
  },
  {
    id: 'tenant-quota',
    labelKey: 'Adjust tenant quota',
    descriptionKey:
      'Adds or subtracts tenant quota with a unique order number.',
    method: 'POST',
    path: '/api/novapay/tenant/{{tenant_key}}/quota',
    headers: WRITE_HEADERS,
    body: JSON.stringify(
      {
        request_id: '{{request_id}}',
        order_no: '{{order_no}}',
        delta_quota: 100,
        reason: 'nova-test-page',
      },
      null,
      2
    ),
    category: 'management',
  },
  {
    id: 'disable-tenant',
    labelKey: 'Disable tenant',
    descriptionKey: 'Disables the tenant and all of its tokens.',
    method: 'POST',
    path: '/api/novapay/tenant/{{tenant_key}}/disable',
    headers: WRITE_HEADERS,
    body: JSON.stringify({ request_id: '{{request_id}}' }, null, 2),
    category: 'management',
  },
  {
    id: 'enable-tenant',
    labelKey: 'Enable tenant',
    descriptionKey:
      'Enables the tenant without re-enabling individually disabled tokens.',
    method: 'POST',
    path: '/api/novapay/tenant/{{tenant_key}}/enable',
    headers: WRITE_HEADERS,
    body: JSON.stringify({ request_id: '{{request_id}}' }, null, 2),
    category: 'management',
  },
  {
    id: 'rotate-token',
    labelKey: 'Rotate tenant key',
    descriptionKey: 'Rotates the primary token secret; the old key stops working immediately.',
    method: 'POST',
    path: '/api/novapay/tenant/{{tenant_key}}/token/rotate',
    headers: WRITE_HEADERS,
    body: JSON.stringify(
      {
        reason: 'suspected leak',
        request_id: '{{request_id}}',
      },
      null,
      2
    ),
    category: 'management',
  },
  {
    id: 'list-tokens',
    labelKey: 'Query tenant keys',
    descriptionKey: 'Lists tenant API keys including the full secret.',
    method: 'GET',
    path: '/api/novapay/tenant/{{tenant_key}}/tokens',
    headers: JSON_HEADERS,
    body: '',
    category: 'management',
  },
  {
    id: 'token-quota',
    labelKey: 'Set token quota',
    descriptionKey: 'Sets remain_quota and unlimited_quota for the token named in the URL.',
    method: 'POST',
    path: '/api/novapay/tenant/{{tenant_key}}/tokens/{{token_name}}/quota',
    headers: WRITE_HEADERS,
    body: JSON.stringify(
      {
        remain_quota: 500000,
        unlimited_quota: false,
        reason: 'employee quota',
        request_id: '{{request_id}}',
      },
      null,
      2
    ),
    category: 'management',
  },
  {
    id: 'delete-token',
    labelKey: 'Revoke token',
    descriptionKey: 'Disables the token named in the editable request URL.',
    method: 'DELETE',
    path: '/api/novapay/tenant/{{tenant_key}}/tokens/{{token_name}}',
    headers: WRITE_HEADERS,
    body: JSON.stringify({ request_id: '{{request_id}}' }, null, 2),
    category: 'management',
  },
  {
    id: 'logs',
    labelKey: 'Usage ledger',
    descriptionKey:
      'Reads successful Nova usage events for ledger and MQ reconciliation.',
    method: 'GET',
    path: '/api/novapay/tenant/{{tenant_key}}/logs?page=1&page_size=20',
    headers: JSON_HEADERS,
    body: '',
    category: 'management',
  },
  {
    id: 'delete-tenant',
    labelKey: 'Soft-delete tenant',
    descriptionKey:
      'Soft-deletes the tenant while preserving its usage history.',
    method: 'DELETE',
    path: '/api/novapay/tenant/{{tenant_key}}',
    headers: WRITE_HEADERS,
    body: JSON.stringify({ request_id: '{{request_id}}' }, null, 2),
    category: 'management',
  },
  {
    id: 'chat-completion',
    labelKey: 'Chat completion and usage message',
    descriptionKey:
      'Calls the selected model and produces a Nova usage event after settlement.',
    method: 'POST',
    path: '/v1/chat/completions',
    headers: JSON.stringify(
      {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        Authorization: 'Bearer {{api_token}}',
        'X-Nova-Tenant-Key': '{{tenant_key}}',
        'X-Nova-Request-Id': '{{nova_request_id}}',
      },
      null,
      2
    ),
    body: JSON.stringify(
      {
        model: '{{model}}',
        messages: [{ role: 'user', content: 'Reply with ok only' }],
        max_tokens: 8,
        stream: false,
      },
      null,
      2
    ),
    category: 'model',
  },
]
