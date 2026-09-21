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

import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { NovaResultsPanel } from '../components/nova-results-panel'
import type { NovaRequestResult } from '../types'

const result: NovaRequestResult = {
  id: 'call-1',
  label: 'Health check',
  method: 'GET',
  url: 'http://127.0.0.1:3000/api/novapay/health',
  ok: true,
  status: 200,
  durationMs: 12,
  timestamp: '2026-01-01T00:00:00.000Z',
  canonical: 'GET\n/api/novapay/health',
  requestHeaders: { 'X-Nova-Signature': 'abc' },
  requestBody: '',
  responseHeaders: { 'content-type': 'application/json' },
  responseBody: '{"success":true}',
}

describe('Nova results panel', () => {
  it('shows the recorded request and response in separate panes', async () => {
    const user = userEvent.setup()
    render(<NovaResultsPanel results={[result]} onClear={vi.fn()} />)

    await user.click(screen.getByText('Health check'))

    const request = screen.getByRole('region', { name: 'Request' })
    const response = screen.getByRole('region', { name: 'Response' })
    expect(within(request).getByText('X-Nova-Signature: abc')).toBeVisible()
    expect(within(response).queryByText('X-Nova-Signature: abc')).not.toBeInTheDocument()

    await user.click(within(response).getByRole('tab', { name: 'Response body' }))
    expect(within(response).getByText(/"success": true/)).toBeVisible()
  })
})
