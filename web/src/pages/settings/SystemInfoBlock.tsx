// 系统信息块（FR-94 骨架）：预留「版本与更新」「控制面健康」子 tab 空壳容器（占位文案）。
// 内容由后续 FR 填：版本与更新 → FR-100/99；控制面健康 → FR-95（并入 /system 页）。
// 子 tab 选择落 search param（?tab=version|health），深链 / 刷新保持 / 后退。
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import PlaceholderTab from './PlaceholderTab'

// 子 tab 定义：标题键 + 占位文案键。
const TABS: Array<{ value: string; labelKey: string; placeholderKey: string }> = [
  {
    value: 'version',
    labelKey: 'settingsAggregate.tabVersion',
    placeholderKey: 'settingsAggregate.placeholderVersion',
  },
  {
    value: 'health',
    labelKey: 'settingsAggregate.tabHealth',
    placeholderKey: 'settingsAggregate.placeholderHealth',
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
          <PlaceholderTab text={t(tab.placeholderKey)} />
        </TabsContent>
      ))}
    </Tabs>
  )
}
