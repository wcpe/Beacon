// 系统信息块（FR-94 骨架 + FR-95 折叠）：
// 「版本与更新」仍为空壳占位（待 FR-100/99 接入）；「控制面健康」折叠复用 SystemObservabilityPage（FR-82）。
// 非激活子 tab 依赖 Radix Tabs 默认卸载（不 forceMount），使控制面健康页 5s 轮询切走时自然停。
// 子 tab 选择落 search param（?tab=version|health），深链 / 刷新保持 / 后退。
import type { ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import PlaceholderTab from './PlaceholderTab'
import SystemObservabilityPage from '../SystemObservabilityPage'

// 子 tab 定义：标题键 + 内容渲染器（占位文案 → PlaceholderTab；折叠页 → 直接复用原页组件）。
const TABS: Array<{ value: string; labelKey: string; render: (t: (k: string) => string) => ReactNode }> = [
  {
    value: 'version',
    labelKey: 'settingsAggregate.tabVersion',
    render: (t) => <PlaceholderTab text={t('settingsAggregate.placeholderVersion')} />,
  },
  {
    value: 'health',
    labelKey: 'settingsAggregate.tabHealth',
    // 折叠原 /system 控制面健康页（FR-95）：自包含默认导出组件直接作子 tab 内容渲染。
    render: () => <SystemObservabilityPage />,
  },
]
const TAB_VALUES = TABS.map((t) => t.value)
const DEFAULT_TAB = TABS[0].value

export default function SystemInfoBlock() {
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
