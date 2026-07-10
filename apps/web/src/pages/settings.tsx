// 运维设置页（/settings）：左侧吸顶分区导航 + 右侧对应分区内容（分区切换不滚长页）。
// 三大块：运行参数（热改项）/ 健康权重（编辑 + rev 历史）/ 归档清理（总览 + 任务主从）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Archive, Gauge, SlidersHorizontal } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { SectionHeader, cn } from '@beacon/ui'

import ArchiveBlock from './settings/archive-block'
import SettingsBlock from './settings/settings-block'
import WeightsBlock from './settings/weights-block'

type SectionKey = 'params' | 'weights' | 'archive'

export default function SettingsPage() {
  const { t } = useTranslation()
  const [active, setActive] = useState<SectionKey>('params')

  const sections: { key: SectionKey; label: string; icon: LucideIcon }[] = [
    { key: 'params', label: t('system.settings.paramsTitle'), icon: SlidersHorizontal },
    { key: 'weights', label: t('system.settings.weightsSection'), icon: Gauge },
    { key: 'archive', label: t('system.settings.archiveSection'), icon: Archive },
  ]

  return (
    <section className="grid gap-4">
      <SectionHeader size="lg" icon={<SlidersHorizontal className="size-5" />} title={t('nav.settings')} />
      <div className="grid gap-4 lg:grid-cols-[13rem_minmax(0,1fr)]">
        {/* 左侧吸顶分区导航：切换分区只换右侧内容，不滚长页 */}
        <nav
          aria-label={t('nav.settings')}
          className="self-start lg:sticky lg:top-0 grid gap-1 rounded-xl border border-border bg-card p-2 shadow-card"
        >
          {sections.map((s) => {
            const Icon = s.icon
            const on = active === s.key
            return (
              <button
                key={s.key}
                type="button"
                aria-current={on ? 'page' : undefined}
                onClick={() => {
                  setActive(s.key)
                }}
                className={cn(
                  'flex items-center gap-2 rounded-lg px-3 py-2 text-left text-[13px] font-medium transition-colors',
                  on ? 'bg-brand-50 text-brand' : 'text-ink-2 hover:bg-surface-2',
                )}
              >
                <Icon className="size-4 shrink-0" aria-hidden />
                {s.label}
              </button>
            )
          })}
        </nav>

        {/* 右侧分区内容：仅渲染当前分区 */}
        <div className="min-w-0">
          {active === 'params' && <SettingsBlock />}
          {active === 'weights' && <WeightsBlock />}
          {active === 'archive' && <ArchiveBlock />}
        </div>
      </div>
    </section>
  )
}
