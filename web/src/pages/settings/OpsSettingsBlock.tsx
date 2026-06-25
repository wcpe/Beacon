// 运维设置块（FR-94，沿用 FR-62/FR-77）：把原 SettingsPage 的 6 域分组改为 6 个子 tab 呈现，
// 但**保留顶层集中草稿 + dirty 计算 + 批量保存 saveAll + 逐项恢复默认**——
// 草稿 / 脏项 / 批量保存仍跨子 tab 全局统观（不把草稿态下沉进单个子 tab），不回归 FR-62/FR-77。
// 子 tab 选择落 search param（如 ?tab=health），满足深链 / 刷新保持 / 后退。
// 逐项编辑控件 / 草稿 / 批量保存范式上提至 settingsEditing 共享，避免与 SystemConfigBlock 复制（FR-101）。

import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { SettingView } from '../../api/types'
import AsyncSection from '@/components/AsyncSection'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  prefixOf,
  useSettingsDraft,
  SettingRow,
  SettingsSaveBar,
} from './settingsEditing'

// 6 个运维域子 tab（前缀 = 设置项 key 第一段；标题复用既有 settings.group* 键不重复造）。
const OPS_TABS: Array<{ prefix: string; labelKey: string }> = [
  { prefix: 'health', labelKey: 'settings.groupHealth' },
  { prefix: 'metric', labelKey: 'settings.groupMetric' },
  { prefix: 'longpoll', labelKey: 'settings.groupLongpoll' },
  { prefix: 'alert', labelKey: 'settings.groupAlert' },
  { prefix: 'log', labelKey: 'settings.groupLog' },
  { prefix: 'reverse-fetch', labelKey: 'settings.groupReverseFetch' },
]
const OPS_TAB_PREFIXES = OPS_TABS.map((t) => t.prefix)
const DEFAULT_OPS_TAB = OPS_TABS[0].prefix

export default function OpsSettingsBlock() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()

  const { items, isLoading, isError, error, draftOf, setDraft, dirtyItems, savingAll, saveAll } =
    useSettingsDraft()

  // 子 tab 选择来自 search param（非法值回落首 tab），切换时写回 URL——深链 / 刷新保持 / 后退。
  const rawTab = searchParams.get('tab') ?? ''
  const activeTab = OPS_TAB_PREFIXES.includes(rawTab) ? rawTab : DEFAULT_OPS_TAB
  const onTabChange = (next: string) => {
    setSearchParams(
      (prev) => {
        const sp = new URLSearchParams(prev)
        sp.set('tab', next)
        return sp
      },
      { replace: true },
    )
  }

  // 按 6 个固定子 tab 前缀把设置项分桶（保持后端返回的相对顺序）。
  const itemsByPrefix = useMemo(() => {
    const map = new Map<string, SettingView[]>()
    for (const it of items) {
      const p = prefixOf(it.key)
      if (!map.has(p)) map.set(p, [])
      map.get(p)!.push(it)
    }
    return map
  }, [items])

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      {/* 启动 / 安全项在 config.yml 的说明：登录后不可见、此处不可改 */}
      <p className="shrink-0 text-sm text-muted-foreground">{t('settings.configYmlNotice')}</p>

      <AsyncSection isLoading={isLoading} isError={isError} error={error}>
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('settings.empty')}</p>
        ) : (
          <Tabs
            value={activeTab}
            onValueChange={onTabChange}
            className="flex min-h-0 flex-1 flex-col gap-3"
          >
            {/* 6 域子 tab 栏：常驻不滚动 */}
            <TabsList className="w-fit shrink-0">
              {OPS_TABS.map((tab) => (
                <TabsTrigger key={tab.prefix} value={tab.prefix}>
                  {t(tab.labelKey)}
                </TabsTrigger>
              ))}
            </TabsList>

            {/* 内容区局部滚动：仅当前子 tab 的项渲染 */}
            {OPS_TABS.map((tab) => (
              <TabsContent
                key={tab.prefix}
                value={tab.prefix}
                className="min-h-0 flex-1 overflow-y-auto"
              >
                <Card>
                  <CardContent className="divide-y">
                    {(itemsByPrefix.get(tab.prefix) ?? []).map((item) => (
                      <SettingRow
                        key={item.key}
                        item={item}
                        draft={draftOf(item)}
                        onChange={(v) => setDraft(item.key, v)}
                        batchSaving={savingAll}
                      />
                    ))}
                  </CardContent>
                </Card>
              </TabsContent>
            ))}

            {/* 块内 sticky 底栏：跨子 tab 统一改动摘要 + 批量保存（脏项汇总全部子 tab） */}
            <SettingsSaveBar
              dirtyItems={dirtyItems}
              draftOf={draftOf}
              savingAll={savingAll}
              saveAll={saveAll}
              summaryTestId="change-summary"
            />
          </Tabs>
        )}
      </AsyncSection>
    </div>
  )
}
