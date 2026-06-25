// 系统设置块（FR-94 骨架 + FR-95 折叠）：
// 「网络代理」「更新设置」仍为空壳占位（待 FR-101 接入）；
// 「API 密钥」折叠复用 ApiKeysPage（FR-42）、「环境管理」折叠复用 NamespacesPage（FR-53）。
// 非激活子 tab 依赖 Radix Tabs 默认卸载（不 forceMount）。
// 子 tab 选择落 search param（?tab=proxy|update|api-keys|namespaces），深链 / 刷新保持 / 后退。
import type { ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import PlaceholderTab from './PlaceholderTab'
import ApiKeysPage from '../ApiKeysPage'
import NamespacesPage from '../NamespacesPage'

// 子 tab 定义：标题键 + 内容渲染器（占位文案 → PlaceholderTab；折叠页 → 直接复用原页组件）。
const TABS: Array<{ value: string; labelKey: string; render: (t: (k: string) => string) => ReactNode }> = [
  {
    value: 'proxy',
    labelKey: 'settingsAggregate.tabProxy',
    render: (t) => <PlaceholderTab text={t('settingsAggregate.placeholderProxy')} />,
  },
  {
    value: 'update',
    labelKey: 'settingsAggregate.tabUpdate',
    render: (t) => <PlaceholderTab text={t('settingsAggregate.placeholderUpdate')} />,
  },
  {
    value: 'api-keys',
    labelKey: 'settingsAggregate.tabApiKeys',
    // 折叠原 /api-keys 密钥管理页（FR-95）。
    render: () => <ApiKeysPage />,
  },
  {
    value: 'namespaces',
    labelKey: 'settingsAggregate.tabNamespaces',
    // 折叠原 /namespaces 环境管理页（FR-95）。
    render: () => <NamespacesPage />,
  },
]
const TAB_VALUES = TABS.map((t) => t.value)
const DEFAULT_TAB = TABS[0].value

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

  return (
    <Tabs value={activeTab} onValueChange={onTabChange} className="flex h-full min-h-0 flex-col gap-3">
      <TabsList className="w-fit shrink-0">
        {TABS.map((tab) => (
          <TabsTrigger key={tab.value} value={tab.value}>
            {t(tab.labelKey)}
          </TabsTrigger>
        ))}
      </TabsList>
      {TABS.map((tab) => (
        <TabsContent key={tab.value} value={tab.value} className="min-h-0 flex-1 overflow-y-auto">
          {tab.render(t)}
        </TabsContent>
      ))}
    </Tabs>
  )
}
