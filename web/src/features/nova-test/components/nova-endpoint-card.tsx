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

import { CheckCircle2, CircleX, Loader2, Play } from 'lucide-react'
import { useState } from 'react'
import { useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { NOVA_TEST_NS } from '../i18n'

import { CopyButton } from '@/components/copy-button'
import { JsonCodeEditor } from '@/components/json-code-editor'
import {
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from '@/components/ui/accordion'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { executeNovaRequest } from '../lib/nova-client'
import type { NovaEndpointTemplate } from '../lib/nova-endpoints'
import type { NovaRequestResult, NovaTestConfig } from '../types'

type NovaEndpointCardProps = {
  endpoint: NovaEndpointTemplate
  baseUrl: string
  onResult: (result: NovaRequestResult) => void
}

function replaceVariables(
  source: string,
  variables: Record<string, string>
): string {
  return source.replaceAll(/\{\{([a-z_]+)\}\}/g, (match, key: string) =>
    Object.hasOwn(variables, key) ? variables[key] : match
  )
}

function parseHeaders(source: string): Record<string, string> {
  const parsed = JSON.parse(source) as unknown
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('Headers must be a JSON object.')
  }
  const headers: Record<string, string> = {}
  for (const [key, value] of Object.entries(parsed)) {
    if (typeof value !== 'string') {
      throw new Error('Every header value must be a string.')
    }
    headers[key] = value
  }
  return headers
}

function endpointUrl(baseUrl: string, path: string): string {
  return `${baseUrl.trim().replace(/\/$/, '')}${path.startsWith('/') ? path : `/${path}`}`
}

function captureOneTimeValues(
  endpointId: string,
  responseBody: string,
  form: ReturnType<typeof useFormContext<NovaTestConfig>>
) {
  if (!['create-tenant', 'rotate-token'].includes(endpointId)) return
  try {
    const response = JSON.parse(responseBody) as {
      tenant?: { tenant_key?: unknown }
      token?: { token?: unknown }
    }
    if (typeof response.tenant?.tenant_key === 'string') {
      form.setValue('tenantKey', response.tenant.tenant_key)
    }
    if (typeof response.token?.token === 'string') {
      form.setValue('apiToken', response.token.token)
    }
  } catch {
    // The raw response remains visible for diagnostics.
  }
}

export function NovaEndpointCard(props: NovaEndpointCardProps) {
  const { t } = useTranslation(NOVA_TEST_NS)
  const form = useFormContext<NovaTestConfig>()
  const [urlOverride, setUrlOverride] = useState('')
  const [headers, setHeaders] = useState(props.endpoint.headers)
  const [body, setBody] = useState(props.endpoint.body)
  const [result, setResult] = useState<NovaRequestResult | null>(null)
  const [error, setError] = useState('')
  const [isRunning, setIsRunning] = useState(false)
  const displayedUrl =
    urlOverride || endpointUrl(props.baseUrl, props.endpoint.path)

  const run = async () => {
    setError('')
    setIsRunning(true)
    try {
      const config = form.getValues()
      const runId = globalThis.crypto.randomUUID()
      const variables = {
        tenant_key: config.tenantKey,
        api_token: config.apiToken,
        model: config.model,
        idempotency_key: `nova-test-${runId}`,
        operation_id: `nova-test-${runId}`,
        nova_request_id: `nova-test-${runId}`,
      }
      let target: URL
      try {
        target = new URL(replaceVariables(displayedUrl, variables))
      } catch {
        throw new Error('Request URL is invalid.')
      }
      const requestHeaders = parseHeaders(replaceVariables(headers, variables))
      const requestBody = replaceVariables(body, variables)
      const nextResult = await executeNovaRequest(
        { ...config, baseUrl: target.origin },
        {
          label: t(props.endpoint.labelKey),
          method: props.endpoint.method,
          path: target.pathname,
          query: target.search.slice(1),
          headers: requestHeaders,
          body: requestBody.trim() || undefined,
        }
      )
      setResult(nextResult)
      props.onResult(nextResult)
      if (nextResult.ok) {
        captureOneTimeValues(props.endpoint.id, nextResult.responseBody, form)
      }
    } catch (runError) {
      const message =
        runError instanceof Error ? runError.message : String(runError)
      setError(t(message))
    } finally {
      setIsRunning(false)
    }
  }

  const resultDetail = result
    ? JSON.stringify(
        {
          request: {
            method: result.method,
            url: result.url,
            headers: result.requestHeaders,
            canonical: result.canonical,
            body: result.requestBody || undefined,
          },
          response: {
            status: result.status,
            headers: result.responseHeaders,
            body: result.responseBody,
            error: result.error,
          },
        },
        null,
        2
      )
    : ''

  return (
    <AccordionItem value={props.endpoint.id} className='last:border-b-0'>
      <AccordionTrigger className='px-4 py-3 hover:no-underline'>
        <span className='flex min-w-0 flex-1 items-start gap-3 pr-3'>
          <Badge
            variant='outline'
            className='mt-0.5 min-w-14 justify-center font-mono text-[11px]'
          >
            {props.endpoint.method}
          </Badge>
          <span className='min-w-0'>
            <span className='block font-medium'>
              {t(props.endpoint.labelKey)}
            </span>
            <span className='text-muted-foreground mt-0.5 block truncate font-mono text-xs font-normal'>
              {props.endpoint.path}
            </span>
          </span>
          {result ? (
            <span className='ml-auto flex shrink-0 items-center gap-1 text-xs'>
              {result.ok ? (
                <CheckCircle2 className='size-4 text-emerald-600' />
              ) : (
                <CircleX className='text-destructive size-4' />
              )}
              {result.status ?? t('Network error')}
            </span>
          ) : null}
        </span>
      </AccordionTrigger>
      <AccordionContent className='space-y-4 px-4 pt-2 pb-4'>
        <p className='text-muted-foreground text-sm'>
          {t(props.endpoint.descriptionKey)}
        </p>
        <Field>
          <FieldLabel htmlFor={`nova-url-${props.endpoint.id}`}>
            {t('Request URL and query parameters')}
          </FieldLabel>
          <Input
            id={`nova-url-${props.endpoint.id}`}
            value={displayedUrl}
            onChange={(event) => setUrlOverride(event.target.value)}
            className='font-mono text-xs'
            spellCheck={false}
          />
          <FieldDescription>
            {t('Template variables are replaced when the request is sent.')}
          </FieldDescription>
        </Field>

        <div className='grid gap-4 xl:grid-cols-2'>
          <Field>
            <FieldLabel>{t('Request headers')}</FieldLabel>
            <JsonCodeEditor
              value={headers}
              onChange={setHeaders}
              heightClassName='h-52 min-h-52 max-h-52'
              ariaLabel={t('Request headers')}
            />
            <FieldDescription>
              {t(
                'HMAC signature headers are generated automatically and can be overridden here for negative tests.'
              )}
            </FieldDescription>
          </Field>
          <Field>
            <FieldLabel>{t('Request body')}</FieldLabel>
            <JsonCodeEditor
              value={body}
              onChange={setBody}
              heightClassName='h-52 min-h-52 max-h-52'
              placeholder=''
              ariaLabel={t('Request body')}
            />
          </Field>
        </div>

        <div className='flex flex-wrap items-center gap-2'>
          <Button type='button' onClick={() => void run()} disabled={isRunning}>
            {isRunning ? (
              <Loader2
                data-icon='inline-start'
                className='animate-spin'
                aria-hidden='true'
              />
            ) : (
              <Play data-icon='inline-start' aria-hidden='true' />
            )}
            {isRunning ? t('Sending...') : t('Send this request')}
          </Button>
          <span className='text-muted-foreground text-xs'>
            {'{{tenant_key}} · {{api_token}} · {{model}} · {{idempotency_key}}'}
          </span>
        </div>

        {error ? (
          <Alert variant='destructive'>
            <CircleX aria-hidden='true' />
            <AlertTitle>{t('Request could not be sent')}</AlertTitle>
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        ) : null}

        {result ? (
          <div className='relative overflow-hidden rounded-lg border'>
            <div className='bg-muted/40 flex items-center gap-2 border-b px-3 py-2'>
              {result.ok ? (
                <CheckCircle2 className='size-4 text-emerald-600' />
              ) : (
                <CircleX className='text-destructive size-4' />
              )}
              <span className='font-medium'>{t('Request and response')}</span>
              <span className='text-muted-foreground text-xs'>
                HTTP {result.status ?? t('Network error')} · {result.durationMs}{' '}
                ms
              </span>
              <CopyButton value={resultDetail} className='ml-auto' />
            </div>
            <pre className='max-h-[520px] overflow-auto p-3 font-mono text-xs break-all whitespace-pre-wrap'>
              {resultDetail}
            </pre>
          </div>
        ) : null}
      </AccordionContent>
    </AccordionItem>
  )
}
