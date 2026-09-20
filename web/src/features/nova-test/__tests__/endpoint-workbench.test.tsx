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

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FormProvider, useForm } from 'react-hook-form'
import { describe, expect, it, vi } from 'vitest'

import { NovaEndpointWorkbench } from '../components/nova-endpoint-workbench'
import type { NovaTestConfig } from '../types'

const config: NovaTestConfig = {
  baseUrl: 'http://127.0.0.1:3000',
  keyId: 'current',
  hmacSecret: '',
  tenantKey: 'tenant-a',
  apiToken: 'sk-test',
  model: 'deepseek-v3',
  amqpUrl: '',
  managementUrl: 'http://127.0.0.1:15672',
  mqUsername: '',
  mqPassword: '',
  vhost: '/',
  exchange: 'nova.events',
  routingKey: 'nova.usage.reported',
  queue: 'nova.test.consumer',
  requeue: false,
}

function Fixture() {
  const form = useForm<NovaTestConfig>({ defaultValues: config })
  return (
    <FormProvider {...form}>
      <NovaEndpointWorkbench onResult={vi.fn()} />
    </FormProvider>
  )
}

describe('Nova endpoint workbench', () => {
  it('keeps endpoints collapsed until the user expands one', async () => {
    const user = userEvent.setup()
    render(<Fixture />)

    const healthTrigger = screen.getByRole('button', { name: /Health check/ })
    expect(healthTrigger).toHaveAttribute('aria-expanded', 'false')
    expect(
      screen.queryByLabelText('Request URL and query parameters')
    ).not.toBeInTheDocument()

    await user.click(healthTrigger)

    expect(healthTrigger).toHaveAttribute('aria-expanded', 'true')
    expect(
      screen.getByLabelText('Request URL and query parameters')
    ).toHaveValue('http://127.0.0.1:3000/api/novapay/health')
    expect(screen.getByLabelText('Request headers')).toBeVisible()
  })

  it('shows model usage as a separate editable request', async () => {
    const user = userEvent.setup()
    render(<Fixture />)

    const modelTrigger = screen.getByRole('button', {
      name: /Chat completion and usage message/,
    })
    await user.click(modelTrigger)

    expect(
      screen.getByLabelText('Request URL and query parameters')
    ).toHaveValue('http://127.0.0.1:3000/v1/chat/completions')
    expect(
      screen.getByRole('button', { name: 'Send this request' })
    ).toBeEnabled()
  })
})
