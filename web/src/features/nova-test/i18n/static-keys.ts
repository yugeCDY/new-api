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

/** Dynamic Nova-test keys that are not always visible as t('...') literals. */
export const NOVA_TEST_STATIC_I18N_KEYS = [
  'Enter a valid Management API URL',
  'Enter a valid New-API URL',
  'Exchange is required',
  'HMAC key ID is required',
  'HMAC secret is required',
  'Queue name is required',
  'Routing key is required',
  'Required',
  'Health check',
  'Checks the Nova module, database, outbox, and RabbitMQ status.',
  'Model catalog',
  'Reads models and Nova pricing information available to tenants.',
  'List tenants',
  'Lists Nova tenants with editable pagination parameters.',
  'Create tenant',
  'Creates a tenant and returns its initial token once.',
  'Get tenant',
  'Reads the current tenant status and remaining quota.',
  'Update tenant',
  'Updates the tenant display name and metadata.',
  'Adjust tenant quota',
  'Adds or subtracts tenant quota with a unique operation ID.',
  'Disable tenant',
  'Disables the tenant and all of its tokens.',
  'Enable tenant',
  'Enables the tenant without re-enabling individually disabled tokens.',
  'Rotate tenant token',
  'Creates a replacement token and disables the old token.',
  'List tenant tokens',
  'Lists masked tokens and their current status and quota.',
  'Adjust token quota',
  'Adds or subtracts quota for the token named in the URL.',
  'Revoke token',
  'Disables the token named in the editable request URL.',
  'Usage ledger',
  'Reads successful Nova usage events for ledger and MQ reconciliation.',
  'Soft-delete tenant',
  'Soft-deletes the tenant while preserving its usage history.',
  'Chat completion and usage message',
  'Calls the selected model and produces a Nova usage event after settlement.',
] as const
