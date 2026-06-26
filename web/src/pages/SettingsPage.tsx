// 运维设置单页（FR-62/FR-77，ADR-0048 拍平回单页；FR-108 横 tab 改锚点 rail）：
// 6 个 key 前缀域（health/metric/longpoll/alert/log/reverse-fetch）以左侧 sticky 分区锚点 rail + scroll-spy 呈现，
// 各域全部渲染、设置行去 Card 外壳；顶层集中草稿 + dirty 计算 + 批量保存 saveAll + 逐项恢复默认（跨域统观全部脏项）。
// 注意：update.* 更新相关项不在本页，挪到「版本与更新」页（/system/version）。
// 逐项编辑控件 / 草稿 / 批量保存范式复用 settingsEditing 共享原语。

import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import type { SettingView } from '../api/types'
import AsyncSection from '@/components/AsyncSection'
import { usePageHeader } from '@/components/PageHeader'
import AnchorRailLayout, { AnchorSectionBlock, type AnchorSection } from '@/components/AnchorRailLayout'
import {
  prefixOf,
  useSettingsDraft,
  SettingRow,
  SettingsSaveBar,
} from './settings/settingsEditing'

// 6 个运维域分区（前缀 = 设置项 key 第一段；标题复用既有 settings.group* 键）。
const OPS_SECTIONS: Array<{ prefix: string; labelKey: string }> = [
  { prefix: 'health', labelKey: 'settings.groupHealth' },
  { prefix: 'metric', labelKey: 'settings.groupMetric' },
  { prefix: 'longpoll', labelKey: 'settings.groupLongpoll' },
  { prefix: 'alert', labelKey: 'settings.groupAlert' },
  { prefix: 'log', labelKey: 'settings.groupLog' },
  { prefix: 'reverse-fetch', labelKey: 'settings.groupReverseFetch' },
]

export default function SettingsPage() {
  const { t } = useTranslation()

  const { items, isLoading, isError, error, draftOf, setDraft, dirtyItems, savingAll, saveAll } =
    useSettingsDraft()

  // 按 6 个固定域前缀把设置项分桶（保持后端返回的相对顺序）。
  const itemsByPrefix = useMemo(() => {
    const map = new Map<string, SettingView[]>()
    for (const it of items) {
      const p = prefixOf(it.key)
      if (!map.has(p)) map.set(p, [])
      map.get(p)!.push(it)
    }
    return map
  }, [items])

  // 锚点 rail 分区（FR-108）：仅列有设置项的域，避免空分区。
  const railSections: AnchorSection[] = OPS_SECTIONS.filter(
    (s) => (itemsByPrefix.get(s.prefix)?.length ?? 0) > 0,
  ).map((s) => ({ id: `settings-${s.prefix}`, label: t(s.labelKey) }))

  // 页眉（FR-105）：标题 + 副标题（config.yml 说明），系统页非环境范围
  usePageHeader({
    title: t('settings.title'),
    subtitle: t('settings.configYmlNotice'),
    envScoped: false,
  })

  return (
    <div className="flex h-full min-h-0 flex-col gap-4">
      <AsyncSection isLoading={isLoading} isError={isError} error={error}>
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('settings.empty')}</p>
        ) : (
          <>
            {/* 左 sticky 锚点 rail + scroll-spy：各域分区全部渲染，设置行去 Card 外壳（细线分隔） */}
            <AnchorRailLayout sections={railSections} ariaLabel={t('settings.railAria')}>
              {OPS_SECTIONS.map((sec) => {
                const secItems = itemsByPrefix.get(sec.prefix) ?? []
                if (secItems.length === 0) return null
                return (
                  <AnchorSectionBlock key={sec.prefix} id={`settings-${sec.prefix}`} title={t(sec.labelKey)}>
                    <div className="divide-y">
                      {secItems.map((item) => (
                        <SettingRow
                          key={item.key}
                          item={item}
                          draft={draftOf(item)}
                          onChange={(v) => setDraft(item.key, v)}
                          batchSaving={savingAll}
                        />
                      ))}
                    </div>
                  </AnchorSectionBlock>
                )
              })}
            </AnchorRailLayout>

            {/* sticky 底栏：跨域统一改动摘要 + 批量保存（脏项汇总全部域分区） */}
            <SettingsSaveBar
              dirtyItems={dirtyItems}
              draftOf={draftOf}
              savingAll={savingAll}
              saveAll={saveAll}
              summaryTestId="change-summary"
            />
          </>
        )}
      </AsyncSection>
    </div>
  )
}
