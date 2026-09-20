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

import { CheckCircle2, SlidersHorizontal } from 'lucide-react'
import { useState } from 'react'
import { useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { NOVA_TEST_NS } from '../i18n'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

import { parseNovaEnvironment } from '../lib/nova-client'
import type { NovaTestConfig } from '../types'

const ENV_PLACEHOLDER = `NOVA_BASE_URL=http://127.0.0.1:3000
NOVA_HMAC_KEYS=current:<base64url-secret>
NOVA_TENANT_KEY=<tenant-key>
NOVA_TOKEN=<sk-token>
NOVA_TEST_MODEL=deepseek-v3
RABBITMQ_URL=amqp://user:password@127.0.0.1:5672/`

export function NovaEnvironmentSetup() {
  const { t } = useTranslation(NOVA_TEST_NS)
  const form = useFormContext<NovaTestConfig>()
  const [environment, setEnvironment] = useState('')
  const [applied, setApplied] = useState(false)

  const apply = () => {
    const imported = parseNovaEnvironment(environment)
    form.reset({ ...form.getValues(), ...imported })
    setApplied(true)
  }

  return (
    <Card className='border-primary/25 shadow-sm'>
      <CardHeader>
        <CardTitle className='flex items-center gap-2'>
          <SlidersHorizontal
            className='text-primary size-4'
            aria-hidden='true'
          />
          {t('Connection configuration')}
        </CardTitle>
        <CardDescription>
          {t(
            'Paste the deployment environment variables once. Every endpoint below will use them as editable defaults.'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='grid gap-4 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-end'>
        <Field>
          <FieldLabel htmlFor='nova-environment'>
            {t('Nova environment variables')}
          </FieldLabel>
          <Textarea
            id='nova-environment'
            value={environment}
            onChange={(event) => {
              setEnvironment(event.target.value)
              setApplied(false)
            }}
            placeholder={ENV_PLACEHOLDER}
            className='min-h-40 resize-y font-mono text-xs'
            spellCheck={false}
            autoComplete='off'
          />
          <FieldDescription>
            {t(
              'Bash export, PowerShell $env:, and plain KEY=value formats are supported. Secrets are not saved.'
            )}
          </FieldDescription>
        </Field>
        <div className='space-y-3 lg:w-56'>
          <Button
            type='button'
            className='w-full'
            onClick={apply}
            disabled={!environment.trim()}
          >
            <SlidersHorizontal data-icon='inline-start' aria-hidden='true' />
            {t('Apply to all endpoints')}
          </Button>
          {applied ? (
            <Alert className='py-2.5'>
              <CheckCircle2 aria-hidden='true' />
              <AlertTitle>{t('Configuration applied')}</AlertTitle>
              <AlertDescription>
                {t('You can now expand and run any endpoint.')}
              </AlertDescription>
            </Alert>
          ) : null}
        </div>
      </CardContent>
    </Card>
  )
}
