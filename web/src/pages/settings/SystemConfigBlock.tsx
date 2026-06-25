// 系统设置块（FR-94 骨架 + FR-95 折叠 + FR-101 填实）：
// 「网络代理」编辑 update.proxy-url（FR-98 脱敏回显口径）、「更新设置」编辑 update.channel/auto-check-enabled/check-interval-hours（FR-101），
// 两子 tab 复用 settingsEditing 的逐项编辑 + 草稿 + 批量保存范式，并**共享一份草稿与批量保存条**（跨子 tab 统一，不回归 FR-77）；
// 「API 密钥」折叠复用 ApiKeysPage（FR-42）、「环境管理」折叠复用 NamespacesPage（FR-53）。
// 非激活子 tab 依赖 Radix Tabs 默认卸载（不 forceMount）。
// 子 tab 选择落 search param（?tab=proxy|update|api-keys|namespaces），深链 / 刷新保持 / 后退。
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { SettingView } from '../../api/types'
import AsyncSection from '@/components/AsyncSection'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import PlaceholderTab from './PlaceholderTab'
import ApiKeysPage from '../ApiKeysPage'
import NamespacesPage from '../NamespacesPage'
import { useSettingsDraft, SettingRow, SettingsSaveBar } from './settingsEditing'

// 网络代理子 tab 管的设置 key（FR-98）。
const PROXY_KEYS = ['update.proxy-url']
// 更新设置子 tab 管的设置 key（FR-101）。
const UPDATE_KEYS = ['update.channel', 'update.auto-check-enabled', 'update.check-interval-hours']

const TAB_VALUES = ['proxy', 'update', 'api-keys', 'namespaces']
const DEFAULT_TAB = 'proxy'

export default function SystemConfigBlock() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()

  const raw = searchParams.get('tab') ?? ''
  const activeTab = TAB_VALUES.includes(raw) ? raw : DEFAULT_TAB
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

  // 网络代理 + 更新设置两子 tab 共享同一份草稿与批量保存（跨子 tab 统一，FR-77 范式）。
  const { items, isLoading, isError, error, draftOf, setDraft, dirtyItems, savingAll, saveAll } =
    useSettingsDraft()

  // 仅取本块管的设置项（其余如运维域项不在本块呈现，也不计入本块脏项 / 批量保存）。
  const managedKeys = new Set([...PROXY_KEYS, ...UPDATE_KEYS])
  const pick = (keys: string[]) => items.filter((it) => keys.includes(it.key))
  const blockDirty = dirtyItems.filter((it) => managedKeys.has(it.key))

  return (
    <Tabs value={activeTab} onValueChange={onTabChange} className="flex h-full min-h-0 flex-col gap-3">
      <TabsList className="w-fit shrink-0">
        <TabsTrigger value="proxy">{t('settingsAggregate.tabProxy')}</TabsTrigger>
        <TabsTrigger value="update">{t('settingsAggregate.tabUpdate')}</TabsTrigger>
        <TabsTrigger value="api-keys">{t('settingsAggregate.tabApiKeys')}</TabsTrigger>
        <TabsTrigger value="namespaces">{t('settingsAggregate.tabNamespaces')}</TabsTrigger>
      </TabsList>

      {/* 网络代理：编辑 update.proxy-url（FR-98 脱敏回显，未改不回传覆盖原值） */}
      <TabsContent value="proxy" className="min-h-0 flex-1 overflow-y-auto">
        <SettingsGroup
          items={pick(PROXY_KEYS)}
          isLoading={isLoading}
          isError={isError}
          error={error}
          draftOf={draftOf}
          setDraft={setDraft}
          savingAll={savingAll}
          emptyHint={t('settingsAggregate.placeholderProxy')}
        />
      </TabsContent>

      {/* 更新设置：编辑 update 渠道 / 自动检查 / 检查周期（FR-101） */}
      <TabsContent value="update" className="min-h-0 flex-1 overflow-y-auto">
        <SettingsGroup
          items={pick(UPDATE_KEYS)}
          isLoading={isLoading}
          isError={isError}
          error={error}
          draftOf={draftOf}
          setDraft={setDraft}
          savingAll={savingAll}
          emptyHint={t('settingsAggregate.placeholderUpdate')}
        />
      </TabsContent>

      {/* API 密钥：折叠原 /api-keys 密钥管理页（FR-95） */}
      <TabsContent value="api-keys" className="min-h-0 flex-1 overflow-y-auto">
        <ApiKeysPage />
      </TabsContent>

      {/* 环境管理：折叠原 /namespaces 环境管理页（FR-95） */}
      <TabsContent value="namespaces" className="min-h-0 flex-1 overflow-y-auto">
        <NamespacesPage />
      </TabsContent>

      {/* 块内 sticky 底栏：跨网络代理 + 更新设置两子 tab 统一改动摘要 + 批量保存（仅当两设置 tab 之一激活时显示） */}
      {(activeTab === 'proxy' || activeTab === 'update') && (
        <SettingsSaveBar
          dirtyItems={blockDirty}
          draftOf={draftOf}
          savingAll={savingAll}
          saveAll={saveAll}
          summaryTestId="system-config-change-summary"
        />
      )}
    </Tabs>
  )
}

// 单组设置项渲染：加载三态 + 卡片化逐项编辑行；项为空时退化占位文案（如后端尚未含该项）。
function SettingsGroup({
  items,
  isLoading,
  isError,
  error,
  draftOf,
  setDraft,
  savingAll,
  emptyHint,
}: {
  items: SettingView[]
  isLoading: boolean
  isError: boolean
  error: unknown
  draftOf: (item: SettingView) => string
  setDraft: (key: string, value: string) => void
  savingAll: boolean
  emptyHint: string
}) {
  return (
    <AsyncSection isLoading={isLoading} isError={isError} error={error}>
      {items.length === 0 ? (
        <PlaceholderTab text={emptyHint} />
      ) : (
        <Card>
          <CardContent className="divide-y">
            {items.map((item) => (
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
      )}
    </AsyncSection>
  )
}
