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

import { CheckCircle2, CircleX, Clock3, FileJson2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { cn } from '@/lib/utils'

import type { NovaRequestResult } from '../types'

type NovaResultsPanelProps = {
  results: NovaRequestResult[]
  onClear: () => void
}

export function NovaResultsPanel(props: NovaResultsPanelProps) {
  const { t } = useTranslation()

  return (
    <Card>
      <CardHeader>
        <div className='flex flex-wrap items-start justify-between gap-3'>
          <div>
            <CardTitle>{t('Request timeline')}</CardTitle>
            <CardDescription>
              {t(
                'Inspect canonical strings, redacted headers, and raw responses.'
              )}
            </CardDescription>
          </div>
          {props.results.length ? (
            <Button
              type='button'
              variant='ghost'
              size='sm'
              onClick={props.onClear}
            >
              {t('Clear')}
            </Button>
          ) : null}
        </div>
      </CardHeader>
      <CardContent>
        {props.results.length ? (
          <div className='space-y-2'>
            {props.results.map((result) => {
              const detail = JSON.stringify(
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
              return (
                <details
                  key={result.id}
                  className='bg-muted/20 open:bg-muted/30 rounded-lg border'
                >
                  <summary className='focus-visible:ring-ring/50 flex cursor-pointer list-none flex-wrap items-center gap-2 rounded-lg px-3 py-2 outline-none focus-visible:ring-3'>
                    {result.ok ? (
                      <CheckCircle2
                        className='size-4 text-emerald-600'
                        aria-hidden='true'
                      />
                    ) : (
                      <CircleX
                        className='text-destructive size-4'
                        aria-hidden='true'
                      />
                    )}
                    <span className='font-medium'>{result.label}</span>
                    <span className='bg-muted rounded px-1.5 py-0.5 font-mono text-[11px]'>
                      {result.method}
                    </span>
                    <span
                      className={cn(
                        'rounded px-1.5 py-0.5 font-mono text-[11px]',
                        result.ok
                          ? 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
                          : 'bg-destructive/10 text-destructive'
                      )}
                    >
                      {result.status ?? t('Network error')}
                    </span>
                    <span className='text-muted-foreground ml-auto flex items-center gap-1 text-xs'>
                      <Clock3 className='size-3' aria-hidden='true' />
                      {result.durationMs} ms
                    </span>
                  </summary>
                  <div className='relative border-t'>
                    <CopyButton
                      value={detail}
                      className='bg-background/80 absolute top-2 right-2 z-10'
                    />
                    <pre className='max-h-[480px] overflow-auto p-3 pr-12 font-mono text-xs break-all whitespace-pre-wrap'>
                      {detail}
                    </pre>
                  </div>
                </details>
              )
            })}
          </div>
        ) : (
          <div className='text-muted-foreground flex min-h-44 flex-col items-center justify-center rounded-lg border border-dashed text-center'>
            <FileJson2 className='mb-2 size-7' aria-hidden='true' />
            <p className='text-sm font-medium'>{t('No requests sent yet')}</p>
            <p className='mt-1 text-xs'>
              {t('Run a check or send a signed request to inspect it here.')}
            </p>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
