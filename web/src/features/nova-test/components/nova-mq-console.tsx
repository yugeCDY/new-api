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

import {
  CheckCircle2,
  Inbox,
  Loader2,
  Pause,
  Play,
  RefreshCw,
  Unplug,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Controller, useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { NOVA_TEST_NS } from '../i18n'

import { Button } from '@/components/ui/button'
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
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'

import {
  bindRabbitQueue,
  consumeRabbitMessages,
  declareRabbitQueue,
} from '../lib/nova-client'
import type {
  NovaTestConfig,
  RabbitActionResult,
  RabbitMessage,
} from '../types'

type NovaMqConsoleProps = {
  messages: RabbitMessage[]
  onMessages: (messages: RabbitMessage[]) => void
}

type MqConfigFieldProps = {
  name: 'vhost' | 'queue' | 'exchange' | 'routingKey'
  label: string
}

function MqConfigField(props: MqConfigFieldProps) {
  const { t } = useTranslation(NOVA_TEST_NS)
  const form = useFormContext<NovaTestConfig>()
  const error = form.formState.errors[props.name]
  const id = `nova-mq-${props.name}`

  return (
    <Field data-invalid={Boolean(error)}>
      <FieldLabel htmlFor={id}>{props.label}</FieldLabel>
      <Input
        id={id}
        aria-invalid={Boolean(error)}
        {...form.register(props.name)}
      />
      <FieldError
        errors={error?.message ? [{ message: t(error.message) }] : undefined}
      />
    </Field>
  )
}

const MQ_FIELDS: Array<keyof NovaTestConfig> = [
  'managementUrl',
  'exchange',
  'routingKey',
  'queue',
]

function actionSummary(result: RabbitActionResult | null): string {
  if (!result) return ''
  if (result.error) return result.error
  if (result.status === null) return ''
  return `HTTP ${result.status}${result.body ? ` · ${result.body}` : ''}`
}

export function NovaMqConsole(props: NovaMqConsoleProps) {
  const { t } = useTranslation(NOVA_TEST_NS)
  const form = useFormContext<NovaTestConfig>()
  const [isDeclaring, setIsDeclaring] = useState(false)
  const [isBinding, setIsBinding] = useState(false)
  const [isConsuming, setIsConsuming] = useState(false)
  const [isPolling, setIsPolling] = useState(false)
  const [lastAction, setLastAction] = useState<RabbitActionResult | null>(null)
  const pollingRef = useRef(false)
  const seenEventIdsRef = useRef(new Set<string>())

  useEffect(() => {
    return () => {
      pollingRef.current = false
    }
  }, [])

  const validateMq = async () => form.trigger(MQ_FIELDS)

  const handleDeclare = async () => {
    if (!(await validateMq())) return
    setIsDeclaring(true)
    try {
      const result = await declareRabbitQueue(form.getValues())
      setLastAction(result)
    } finally {
      setIsDeclaring(false)
    }
  }

  const handleBind = async () => {
    if (!(await validateMq())) return
    setIsBinding(true)
    try {
      const result = await bindRabbitQueue(form.getValues())
      setLastAction(result)
    } finally {
      setIsBinding(false)
    }
  }

  const pullMessages = async (): Promise<void> => {
    if (isConsuming) return
    setIsConsuming(true)
    try {
      const consumed = await consumeRabbitMessages(
        form.getValues(),
        seenEventIdsRef.current
      )
      setLastAction(consumed.result)
      if (consumed.messages.length) props.onMessages(consumed.messages)
    } finally {
      setIsConsuming(false)
    }
  }

  const startPolling = async () => {
    if (!(await validateMq())) return
    pollingRef.current = true
    setIsPolling(true)
    while (pollingRef.current) {
      await pullMessages()
      if (!pollingRef.current) break
      await new Promise<void>((resolve) => {
        globalThis.setTimeout(resolve, 3_000)
      })
    }
    setIsPolling(false)
  }

  const stopPolling = () => {
    pollingRef.current = false
    setIsPolling(false)
  }

  const duplicateCount = props.messages.filter(
    (message) => message.duplicate
  ).length

  return (
    <div className='grid gap-4 xl:grid-cols-[minmax(340px,0.8fr)_minmax(0,1.2fr)]'>
      <Card>
        <CardHeader>
          <CardTitle className='flex items-center gap-2'>
            <Unplug className='text-primary size-4' aria-hidden='true' />
            {t('Queue setup')}
          </CardTitle>
          <CardDescription>
            {t(
              'Declare a durable test queue and bind it to the Nova usage routing key.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-5'>
          <FieldGroup>
            <div className='grid gap-5 sm:grid-cols-2'>
              <MqConfigField name='vhost' label={t('Vhost')} />
              <MqConfigField name='queue' label={t('Queue name')} />
            </div>
            <MqConfigField name='exchange' label={t('Exchange')} />
            <MqConfigField name='routingKey' label={t('Routing key')} />
            <Field orientation='horizontal'>
              <div className='flex-1'>
                <FieldLabel htmlFor='nova-mq-requeue'>
                  {t('Requeue messages after reading')}
                </FieldLabel>
                <FieldDescription>
                  {t(
                    'Enable this when inspecting messages without removing them from the queue.'
                  )}
                </FieldDescription>
              </div>
              <Controller
                control={form.control}
                name='requeue'
                render={({ field }) => (
                  <Switch
                    id='nova-mq-requeue'
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                )}
              />
            </Field>
          </FieldGroup>

          <div className='flex flex-wrap gap-2'>
            <Button
              type='button'
              variant='outline'
              onClick={() => void handleDeclare()}
              disabled={isDeclaring}
            >
              {isDeclaring ? (
                <Loader2
                  data-icon='inline-start'
                  className='animate-spin'
                  aria-hidden='true'
                />
              ) : (
                <Inbox data-icon='inline-start' aria-hidden='true' />
              )}
              {t('Declare queue')}
            </Button>
            <Button
              type='button'
              variant='outline'
              onClick={() => void handleBind()}
              disabled={isBinding}
            >
              {isBinding ? (
                <Loader2
                  data-icon='inline-start'
                  className='animate-spin'
                  aria-hidden='true'
                />
              ) : (
                <CheckCircle2 data-icon='inline-start' aria-hidden='true' />
              )}
              {t('Bind queue')}
            </Button>
          </div>

          {lastAction ? (
            <div
              className={cn(
                'rounded-lg border px-3 py-2 font-mono text-xs break-all',
                lastAction.ok
                  ? 'border-emerald-500/30 bg-emerald-500/5 text-emerald-700 dark:text-emerald-300'
                  : 'border-destructive/30 bg-destructive/5 text-destructive'
              )}
              role='status'
            >
              {actionSummary(lastAction)}
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <div className='flex flex-wrap items-start justify-between gap-3'>
            <div>
              <CardTitle>{t('Usage message consumer')}</CardTitle>
              <CardDescription>
                {t(
                  'Polls up to 20 messages every 3 seconds and tracks event_id duplicates.'
                )}
              </CardDescription>
            </div>
            <div className='flex flex-wrap gap-2'>
              <Button
                type='button'
                variant='outline'
                onClick={() => void pullMessages()}
                disabled={isConsuming || isPolling}
              >
                <RefreshCw
                  data-icon='inline-start'
                  className={cn(isConsuming && 'animate-spin')}
                  aria-hidden='true'
                />
                {t('Pull once')}
              </Button>
              {isPolling ? (
                <Button
                  type='button'
                  variant='destructive'
                  onClick={stopPolling}
                >
                  <Pause data-icon='inline-start' aria-hidden='true' />
                  {t('Stop polling')}
                </Button>
              ) : (
                <Button
                  type='button'
                  onClick={() => void startPolling()}
                  disabled={isConsuming}
                >
                  <Play data-icon='inline-start' aria-hidden='true' />
                  {t('Start polling')}
                </Button>
              )}
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <div className='mb-3 flex flex-wrap gap-2 text-xs'>
            <span className='bg-muted rounded-md px-2 py-1'>
              {t('{{count}} messages', { count: props.messages.length })}
            </span>
            <span className='bg-muted rounded-md px-2 py-1'>
              {t('{{count}} duplicates', { count: duplicateCount })}
            </span>
          </div>
          {props.messages.length ? (
            <div className='max-h-[520px] space-y-2 overflow-y-auto pr-1'>
              {props.messages.map((message) => (
                <details
                  key={message.id}
                  className='bg-muted/20 open:bg-muted/30 rounded-lg border'
                >
                  <summary className='focus-visible:ring-ring/50 flex cursor-pointer list-none items-center gap-2 rounded-lg px-3 py-2 outline-none focus-visible:ring-3'>
                    <span
                      className={cn(
                        'size-2 shrink-0 rounded-full',
                        message.duplicate ? 'bg-amber-500' : 'bg-emerald-500'
                      )}
                      aria-hidden='true'
                    />
                    <span className='min-w-0 flex-1 truncate font-mono text-xs'>
                      {message.eventId}
                    </span>
                    <span className='text-muted-foreground shrink-0 text-xs'>
                      {message.parsed?.usage?.model ?? message.routingKey}
                    </span>
                    {message.duplicate ? (
                      <span className='rounded bg-amber-500/10 px-1.5 py-0.5 text-[11px] text-amber-700 dark:text-amber-300'>
                        {t('Duplicate')}
                      </span>
                    ) : null}
                  </summary>
                  <pre className='border-t p-3 font-mono text-xs break-all whitespace-pre-wrap'>
                    {message.parsed
                      ? JSON.stringify(message.parsed, null, 2)
                      : message.payload}
                  </pre>
                </details>
              ))}
            </div>
          ) : (
            <div className='text-muted-foreground flex min-h-48 flex-col items-center justify-center rounded-lg border border-dashed text-center'>
              <Inbox className='mb-2 size-7' aria-hidden='true' />
              <p className='text-sm font-medium'>
                {t('No messages consumed yet')}
              </p>
              <p className='mt-1 max-w-sm text-xs'>
                {t(
                  'Declare and bind the queue before sending a successful business request.'
                )}
              </p>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
