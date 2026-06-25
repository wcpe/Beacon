// 运维设置单页（FR-62/FR-77，ADR-0048 拍平回单页）：
// 6 个 key 前缀域（health/metric/longpoll/alert/log/reverse-fetch）以一级 tab 呈现，
// 顶层集中草稿 + dirty 计算 + 批量保存 saveAll + 逐项恢复默认（跨域统观全部脏项，不下沉进单 tab）。
// 注意：update.* 更新相关项不在本页，挪到「版本与更新」页（/system/version）。
// 逐项编辑控件 / 草稿 / 批量保存范式复用 settingsEditing 共享原语。

import { useMemo } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { SettingView } from '../api/types'
import AsyncSection from '@/components/AsyncSection'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  prefixOf,
  useSettingsDraft,
  SettingRow,
  SettingsSaveBar,
} from './settings/settingsEditing'

// 6 个运维域一级 tab（前缀 = 设置项 key 第一段；标题复用既有 settings.group* 键）。
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

export default function SettingsPage() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()

  const { items, isLoading, isError, error, draftOf, setDraft, dirtyItems, savingAll, saveAll } =
    useSettingsDraft()

  // 域 tab 选择来自 search param（非法值回落首 tab），切换时写回 URL——深链 / 刷新保持 / 后退。
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

  return (
    <div className="flex h-full min-h-0 flex-col gap-4">
      <div className="shrink-0 space-y-1">
        <h1 className="text-xl font-semibold">{t('settings.title')}</h1>
        {/* 启动 / 安全项在 config.yml 的说明：登录后不可见、此处不可改 */}
        <p className="text-sm text-muted-foreground">{t('settings.configYmlNotice')}</p>
      </div>

      <AsyncSection isLoading={isLoading} isError={isError} error={error}>
        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('settings.empty')}</p>
        ) : (
          <Tabs
            value={activeTab}
            onValueChange={onTabChange}
            className="flex min-h-0 flex-1 flex-col gap-3"
          >
            {/* 6 域一级 tab 栏：常驻不滚动 */}
            <TabsList className="w-fit shrink-0">
              {OPS_TABS.map((tab) => (
                <TabsTrigger key={tab.prefix} value={tab.prefix}>
                  {t(tab.labelKey)}
                </TabsTrigger>
              ))}
            </TabsList>

            {/* 内容区局部滚动：仅当前域 tab 的项渲染 */}
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

            {/* sticky 底栏：跨域统一改动摘要 + 批量保存（脏项汇总全部域 tab） */}
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
