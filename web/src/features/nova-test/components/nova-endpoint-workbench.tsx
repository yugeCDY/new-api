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

import { Boxes, Sparkles } from 'lucide-react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { NOVA_TEST_NS } from '../i18n'

import { Accordion } from '@/components/ui/accordion'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { NOVA_ENDPOINTS } from '../lib/nova-endpoints'
import type { NovaRequestResult, NovaTestConfig } from '../types'
import { NovaEndpointCard } from './nova-endpoint-card'

type NovaEndpointWorkbenchProps = {
  onResult: (result: NovaRequestResult) => void
}

export function NovaEndpointWorkbench(props: NovaEndpointWorkbenchProps) {
  const { t } = useTranslation(NOVA_TEST_NS)
  const form = useFormContext<NovaTestConfig>()
  const baseUrl = useWatch({ control: form.control, name: 'baseUrl' })
  const managementEndpoints = NOVA_ENDPOINTS.filter(
    (endpoint) => endpoint.category === 'management'
  )
  const modelEndpoints = NOVA_ENDPOINTS.filter(
    (endpoint) => endpoint.category === 'model'
  )

  return (
    <div className='space-y-4'>
      <Card>
        <CardHeader>
          <CardTitle className='flex items-center gap-2'>
            <Boxes className='text-primary size-4' aria-hidden='true' />
            {t('Nova management endpoints')}
          </CardTitle>
          <CardDescription>
            {t(
              'All endpoints are collapsed by default. Expand one to edit its URL, query parameters, headers, and body before sending.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='p-0'>
          <Accordion multiple>
            {managementEndpoints.map((endpoint) => (
              <NovaEndpointCard
                key={endpoint.id}
                endpoint={endpoint}
                baseUrl={baseUrl}
                onResult={props.onResult}
              />
            ))}
          </Accordion>
        </CardContent>
      </Card>

      <Card className='border-violet-500/25'>
        <CardHeader>
          <CardTitle className='flex items-center gap-2'>
            <Sparkles className='size-4 text-violet-500' aria-hidden='true' />
            {t('Model usage test')}
          </CardTitle>
          <CardDescription>
            {t(
              'Edit the model request, send it, then consume the generated usage message in the MQ panel below.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='p-0'>
          <Accordion multiple>
            {modelEndpoints.map((endpoint) => (
              <NovaEndpointCard
                key={endpoint.id}
                endpoint={endpoint}
                baseUrl={baseUrl}
                onResult={props.onResult}
              />
            ))}
          </Accordion>
        </CardContent>
      </Card>
    </div>
  )
}
