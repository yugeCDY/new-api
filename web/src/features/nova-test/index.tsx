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

import { zodResolver } from '@hookform/resolvers/zod'
import { ChevronDown, Settings2, Sparkles } from 'lucide-react'
import { useState } from 'react'
import { FormProvider, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { NOVA_TEST_NS } from './i18n'
import { z } from 'zod'

import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

import { NovaConfigPanel } from './components/nova-config-panel'
import { NovaEndpointWorkbench } from './components/nova-endpoint-workbench'
import { NovaEnvironmentSetup } from './components/nova-environment-setup'
import { NovaMqConsole } from './components/nova-mq-console'
import { NovaResultsPanel } from './components/nova-results-panel'
import type { NovaRequestResult, NovaTestConfig, RabbitMessage } from './types'

const novaTestConfigSchema = z.object({
  baseUrl: z.url('Enter a valid New-API URL'),
  keyId: z.string(),
  hmacSecret: z.string().trim().min(1, 'HMAC secret is required'),
  tenantKey: z.string().trim().min(1, 'Required'),
  apiToken: z.string().trim().min(1, 'Required'),
  model: z.string().trim().min(1, 'Required'),
  amqpUrl: z.string(),
  managementUrl: z.url('Enter a valid Management API URL'),
  mqUsername: z.string(),
  mqPassword: z.string(),
  vhost: z.string(),
  exchange: z.string().trim().min(1, 'Exchange is required'),
  routingKey: z.string().trim().min(1, 'Routing key is required'),
  queue: z.string().trim().min(1, 'Queue name is required'),
  requeue: z.boolean(),
})

const DEFAULT_CONFIG: NovaTestConfig = {
  baseUrl: 'http://127.0.0.1:3000',
  keyId: '',
  hmacSecret: '',
  tenantKey: '',
  apiToken: '',
  model: 'deepseek-v3',
  amqpUrl: 'amqp://127.0.0.1:5672/',
  managementUrl: 'http://127.0.0.1:15672',
  mqUsername: '',
  mqPassword: '',
  vhost: '/',
  exchange: 'nova.events',
  routingKey: 'nova.usage.reported',
  queue: 'nova.test.consumer',
  requeue: false,
}

export function NovaTest() {
  const { t } = useTranslation(NOVA_TEST_NS)
  const [results, setResults] = useState<NovaRequestResult[]>([])
  const [messages, setMessages] = useState<RabbitMessage[]>([])
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const form = useForm<NovaTestConfig>({
    resolver: zodResolver(novaTestConfigSchema),
    defaultValues: DEFAULT_CONFIG,
    mode: 'onTouched',
  })

  const addResults = (nextResults: NovaRequestResult[]) => {
    setResults((current) => [...nextResults, ...current].slice(0, 50))
  }
  const addMessages = (nextMessages: RabbitMessage[]) => {
    setMessages((current) => [...nextMessages, ...current].slice(0, 100))
  }
  return (
    <FormProvider {...form}>
      <SectionPageLayout>
        <SectionPageLayout.Title>
          <span className='inline-flex min-w-0 items-center gap-2'>
            <span className='truncate'>{t('Nova integration lab')}</span>
            <Badge variant='outline' className='shrink-0'>
              Root
            </Badge>
          </span>
        </SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <div className='mx-auto max-w-[1600px] space-y-5'>
            <section className='from-primary/[0.08] via-background overflow-hidden rounded-xl border bg-gradient-to-br to-violet-500/[0.06] p-5 shadow-xs sm:p-6'>
              <div className='flex max-w-3xl items-start gap-3'>
                <span className='bg-primary/10 text-primary flex size-10 shrink-0 items-center justify-center rounded-xl'>
                  <Sparkles className='size-5' aria-hidden='true' />
                </span>
                <div>
                  <h1 className='text-xl font-semibold tracking-tight sm:text-2xl'>
                    {t('Nova endpoint workbench')}
                  </h1>
                  <p className='text-muted-foreground mt-2 text-sm leading-6'>
                    {t(
                      'Edit the request headers and body above, then read the response below.'
                    )}
                  </p>
                </div>
              </div>
            </section>

            <NovaEnvironmentSetup />

            <NovaEndpointWorkbench
              onResult={(result) => addResults([result])}
            />

            <NovaMqConsole messages={messages} onMessages={addMessages} />

            <NovaResultsPanel
              results={results}
              onClear={() => setResults([])}
            />

            <Collapsible
              open={advancedOpen}
              onOpenChange={setAdvancedOpen}
              className='bg-card rounded-xl border'
            >
              <div className='flex items-center justify-between gap-3 p-4'>
                <div className='flex min-w-0 items-center gap-3'>
                  <Settings2
                    className='text-muted-foreground size-5 shrink-0'
                    aria-hidden='true'
                  />
                  <div>
                    <div className='font-medium'>
                      {t('Advanced manual tools')}
                    </div>
                    <div className='text-muted-foreground text-sm'>
                      {t(
                        'Only open this when you need to change connection or RabbitMQ fields.'
                      )}
                    </div>
                  </div>
                </div>
                <CollapsibleTrigger
                  render={
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      aria-label={
                        advancedOpen
                          ? t('Hide advanced tools')
                          : t('Show advanced tools')
                      }
                    />
                  }
                >
                  {advancedOpen ? t('Hide') : t('Show')}
                  <ChevronDown
                    className={advancedOpen ? 'rotate-180' : undefined}
                    aria-hidden='true'
                  />
                </CollapsibleTrigger>
              </div>
              <CollapsibleContent className='space-y-4 border-t p-4'>
                <NovaConfigPanel />
              </CollapsibleContent>
            </Collapsible>
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
    </FormProvider>
  )
}
