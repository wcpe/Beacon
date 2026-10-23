// 「只能本地配置」只读区（/agent-settings）：把控制面永远无权下发的键摆在明面上。
//
// 这些键的共同点是「权威必须留在被管理端」：接入目标、接入凭据、本机身份标识。
// 它们不进入可编辑列表（没有编辑入口），本区只做两件事——告知边界、解释后果，
// 让运维看到「为什么这里改不了」，而不是以为页面少做了功能。
import { useTranslation } from 'react-i18next'

import { LockKeyhole } from 'lucide-react'

import { Badge } from '@beacon/ui'

import { FORBIDDEN_AGENT_SETTING_KEYS } from './keys'

export default function LocalOnlyCard() {
  const { t } = useTranslation()

  return (
    <div className="grid gap-2 rounded-xl border border-border bg-surface-2 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <LockKeyhole className="size-4 shrink-0 text-ink-3" aria-hidden />
        <h2 className="text-[13px] font-semibold text-ink-1">{t('system.agentSettings.localOnly.title')}</h2>
        <Badge variant="off">{t('system.agentSettings.localOnly.badge')}</Badge>
      </div>
      <p className="text-xs leading-relaxed text-ink-3">{t('system.agentSettings.localOnly.body')}</p>
      <ul className="grid gap-1.5">
        {FORBIDDEN_AGENT_SETTING_KEYS.map((key) => (
          <li
            key={key}
            className="flex flex-wrap items-baseline gap-2 rounded-lg border border-border bg-card px-3 py-2"
          >
            <span className="font-mono text-xs text-ink-2">{key}</span>
            <Badge variant="off">{t('system.agentSettings.localOnly.note')}</Badge>
            <span className="min-w-0 flex-1 text-xs text-ink-4">
              {t(`system.agentSettings.localOnly.descs.${key}`)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}
