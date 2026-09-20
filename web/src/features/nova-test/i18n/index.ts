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

import i18n from '@/i18n/config'

import en from './en.json'
import fr from './fr.json'
import ja from './ja.json'
import ru from './ru.json'
import vi from './vi.json'
import zhTW from './zh-TW.json'
import zhCN from './zh.json'

export const NOVA_TEST_NS = 'novaTest'

const bundles = {
  en,
  zhCN,
  fr,
  ru,
  ja,
  vi,
  zhTW,
} as const

let registered = false

/** Registers Nova test translations without touching shared locale JSON files. */
export function ensureNovaTestI18n(): void {
  if (registered) return
  for (const [lng, bundle] of Object.entries(bundles)) {
    i18n.addResourceBundle(
      lng,
      NOVA_TEST_NS,
      bundle.translation,
      true,
      true
    )
  }
  registered = true
}

ensureNovaTestI18n()
