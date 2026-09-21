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

import { Cable, KeyRound, MessageSquareMore } from 'lucide-react'
import { useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { NOVA_TEST_NS } from '../i18n'

import { PasswordInput } from '@/components/password-input'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import type { NovaTestConfig } from '../types'

type ConfigFieldProps = {
  name: keyof NovaTestConfig
  label: string
  placeholder?: string
  secret?: boolean
  description?: string
}

function ConfigField(props: ConfigFieldProps) {
  const { t } = useTranslation(NOVA_TEST_NS)
  const form = useFormContext<NovaTestConfig>()
  const error = form.formState.errors[props.name]
  const id = `nova-test-${props.name}`
  const inputProps = form.register(props.name)

  return (
    <Field data-invalid={Boolean(error)}>
      <FieldLabel htmlFor={id}>{props.label}</FieldLabel>
      {props.secret ? (
        <PasswordInput
          id={id}
          autoComplete='off'
          spellCheck={false}
          placeholder={props.placeholder}
          aria-invalid={Boolean(error)}
          {...inputProps}
        />
      ) : (
        <Input
          id={id}
          autoComplete='off'
          spellCheck={false}
          placeholder={props.placeholder}
          aria-invalid={Boolean(error)}
          {...inputProps}
        />
      )}
      {props.description ? (
        <FieldDescription>{props.description}</FieldDescription>
      ) : null}
      <FieldError
        errors={error?.message ? [{ message: t(error.message) }] : undefined}
      />
    </Field>
  )
}

export function NovaConfigPanel() {
  const { t } = useTranslation(NOVA_TEST_NS)

  return (
    <div className='grid gap-4 xl:grid-cols-3'>
      <Card>
        <CardHeader>
          <div className='flex items-center gap-2'>
            <span className='bg-primary/10 text-primary flex size-8 items-center justify-center rounded-lg'>
              <Cable className='size-4' aria-hidden='true' />
            </span>
            <div>
              <CardTitle>{t('Nova endpoint')}</CardTitle>
              <CardDescription>
                {t('Requests are sent directly from this browser.')}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <FieldGroup>
            <ConfigField
              name='baseUrl'
              label={t('New-API Base URL')}
              placeholder='http://127.0.0.1:3000'
            />
            <ConfigField
              name='tenantKey'
              label={t('Nova tenant key')}
              placeholder='tenant-test-001'
            />
            <ConfigField
              name='model'
              label={t('Test model')}
              placeholder='deepseek-v3'
            />
          </FieldGroup>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className='flex items-center gap-2'>
            <span className='flex size-8 items-center justify-center rounded-lg bg-amber-500/10 text-amber-600 dark:text-amber-400'>
              <KeyRound className='size-4' aria-hidden='true' />
            </span>
            <div>
              <CardTitle>{t('Request credentials')}</CardTitle>
              <CardDescription>
                {t('Secrets stay in memory and are never saved.')}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <FieldGroup>
            <ConfigField
              name='keyId'
              label={t('HMAC key ID')}
              placeholder='current'
              description={t(
                'Optional. Leave empty to use the server current key.'
              )}
            />
            <ConfigField
              name='hmacSecret'
              label={t('HMAC secret (Base64URL)')}
              secret
            />
            <ConfigField
              name='apiToken'
              label={t('Nova API token')}
              placeholder='sk-...'
              secret
            />
          </FieldGroup>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className='flex items-center gap-2'>
            <span className='flex size-8 items-center justify-center rounded-lg bg-violet-500/10 text-violet-600 dark:text-violet-400'>
              <MessageSquareMore className='size-4' aria-hidden='true' />
            </span>
            <div>
              <CardTitle>{t('RabbitMQ connection')}</CardTitle>
              <CardDescription>
                {t('Message consumption uses the RabbitMQ Management API.')}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <FieldGroup>
            <ConfigField
              name='amqpUrl'
              label={t('AMQP URL')}
              placeholder='amqp://user:password@127.0.0.1:5672/'
              description={t(
                'Credentials and vhost can be read from this URL when the fields below are empty.'
              )}
            />
            <ConfigField
              name='managementUrl'
              label={t('Management API URL')}
              placeholder='http://127.0.0.1:15672'
            />
            <div className='grid gap-5 sm:grid-cols-2'>
              <ConfigField name='mqUsername' label={t('MQ username')} />
              <ConfigField name='mqPassword' label={t('MQ password')} secret />
            </div>
          </FieldGroup>
        </CardContent>
      </Card>
    </div>
  )
}
