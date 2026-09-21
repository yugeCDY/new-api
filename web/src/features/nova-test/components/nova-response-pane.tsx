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

import { useTranslation } from 'react-i18next'

import { NOVA_TEST_NS } from '../i18n'

import { CopyButton } from '@/components/copy-button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

import type { NovaRequestResult } from '../types'

function prettyBody(body: string): string {
  const trimmed = body.trim()
  if (!trimmed) return ''
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return body
  }
}

function formatHeaders(headers: Record<string, string>): string {
  return Object.entries(headers)
    .map(([key, value]) => `${key}: ${value}`)
    .join('\n')
}

function statusClassName(result: NovaRequestResult): string {
  if (result.status === null) return 'bg-destructive/10 text-destructive'
  if (result.ok) return 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
  return 'bg-destructive/10 text-destructive'
}

export function NovaResponsePane(props: { result: NovaRequestResult | null }) {
  const { t } = useTranslation(NOVA_TEST_NS)
  const result = props.result
  const body = result ? prettyBody(result.responseBody) : ''
  const headers = result ? formatHeaders(result.responseHeaders) : ''

  return (
    <section
      aria-label={t('Response')}
      className='overflow-hidden rounded-lg border'
    >
      <div className='bg-muted/40 flex flex-wrap items-center gap-2 border-b px-3 py-2'>
        <span className='text-sm font-medium'>{t('Response')}</span>
        {result ? (
          <>
            <span
              className={cn(
                'rounded px-1.5 py-0.5 font-mono text-[11px]',
                statusClassName(result)
              )}
            >
              {result.status ?? t('Network error')}
            </span>
            <span className='text-muted-foreground font-mono text-xs'>
              {result.durationMs} ms
            </span>
            {body ? <CopyButton value={body} className='ml-auto' /> : null}
          </>
        ) : null}
      </div>
      {result ? (
        <Tabs defaultValue='body'>
          <TabsList variant='line' className='h-9 px-2'>
            <TabsTrigger value='body'>{t('Response body')}</TabsTrigger>
            <TabsTrigger value='headers'>{t('Response headers')}</TabsTrigger>
          </TabsList>
          <TabsContent value='body'>
            <pre className='max-h-80 overflow-auto p-3 font-mono text-xs break-all whitespace-pre-wrap'>
              {result.error && !body
                ? result.error
                : body || t('No response body')}
            </pre>
          </TabsContent>
          <TabsContent value='headers'>
            <pre className='max-h-80 overflow-auto p-3 font-mono text-xs break-all whitespace-pre-wrap'>
              {headers || t('No response headers')}
            </pre>
          </TabsContent>
        </Tabs>
      ) : (
        <p className='text-muted-foreground px-3 py-10 text-center text-sm'>
          {t('Send a request to see the response here.')}
        </p>
      )}
    </section>
  )
}
