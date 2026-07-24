// 页眉语言切换（FR-194）：Globe 下拉中/英，localStorage 持久 + i18n.changeLanguage。
import { useTranslation } from 'react-i18next'
import { Check, Globe } from 'lucide-react'

import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@beacon/ui'

import { i18next } from '../i18n'
import { setLocale, useLocale, type AppLocale } from '../state/locale'

const OPTIONS: { value: AppLocale; labelKey: string }[] = [
  { value: 'zh-CN', labelKey: 'common.header.langZh' },
  { value: 'en', labelKey: 'common.header.langEn' },
]

export default function LanguageMenu() {
  const { t } = useTranslation()
  const locale = useLocale()

  const pick = (next: AppLocale) => {
    setLocale(next)
    void i18next.changeLanguage(next)
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          aria-label={t('common.header.language')}
          data-slot="language-menu-trigger"
        >
          <Globe className="size-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" sideOffset={6} className="min-w-[9rem]">
        {OPTIONS.map((opt) => (
          <DropdownMenuItem
            key={opt.value}
            onSelect={() => {
              pick(opt.value)
            }}
            className="gap-2"
          >
            <span className="flex-1">{t(opt.labelKey)}</span>
            {locale === opt.value ? <Check className="size-3.5 text-brand" aria-hidden /> : null}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
