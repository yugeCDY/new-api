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

import { NOVA_TEST_NS } from '../i18n'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

import type { NovaRequestResult } from '../types'
import { NovaResponsePane } from './nova-response-pane'

type NovaResultsPanelProps = {
  results: NovaRequestResult[]
  onClear: () => void
}

export function NovaResultsPanel(props: NovaResultsPanelProps) {
  const { t } = useTranslation(NOVA_TEST_NS)

  return (
    <Card>
      <CardHeader>
        <div className='flex flex-wrap items-start justify-between gap-3'>
          <div>
            <CardTitle>{t('Request timeline')}</CardTitle>
            <CardDescription>
              {t('Open a call to compare its request and response separately.')}
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
              const requestHeaders = Object.entries(result.requestHeaders)
                .map(([key, value]) => `${key}: ${value}`)
                .join('\n')
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
                  <div className='space-y-3 border-t p-3'>
                    <section aria-label={t('Request')} className='rounded-lg border'>
                      <div className='bg-muted/40 border-b px-3 py-2 text-sm font-medium'>
                        {t('Request')}
                      </div>
                      <Tabs defaultValue='headers'>
                        <TabsList variant='line' className='h-9 px-2'>
                          <TabsTrigger value='headers'>{t('Headers')}</TabsTrigger>
                          <TabsTrigger value='body'>{t('Body')}</TabsTrigger>
                          <TabsTrigger value='signature'>{t('Signature')}</TabsTrigger>
                        </TabsList>
                        <TabsContent value='headers'>
                          <pre className='max-h-48 overflow-auto p-3 font-mono text-xs break-all whitespace-pre-wrap'>
                            {requestHeaders || t('No request headers')}
                          </pre>
                        </TabsContent>
                        <TabsContent value='body'>
                          <pre className='max-h-48 overflow-auto p-3 font-mono text-xs break-all whitespace-pre-wrap'>
                            {result.requestBody || t('No request body')}
                          </pre>
                        </TabsContent>
                        <TabsContent value='signature'>
                          <pre className='max-h-48 overflow-auto p-3 font-mono text-xs break-all whitespace-pre-wrap'>
                            {result.canonical ||
                              t('The signature is generated when the request is sent.')}
                          </pre>
                        </TabsContent>
                      </Tabs>
                    </section>
                    <NovaResponsePane result={result} />
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
