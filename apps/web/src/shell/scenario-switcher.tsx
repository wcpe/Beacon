// 演示模式四态场景切换器（FR-159 前端侧）：切换 mock 场景（空态 / 常规 / 超大量 / 异常）。
// 切换后的全量查询失效由 AppShell 的场景订阅统一处理，本组件只负责改场景。
import { useSyncExternalStore } from 'react'
import { useTranslation } from 'react-i18next'

import {
  getMockScenario,
  MOCK_SCENARIOS,
  setMockScenario,
  subscribeMockScenario,
} from '@beacon/devmock'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'

export default function ScenarioSwitcher() {
  const { t } = useTranslation()
  const scenario = useSyncExternalStore(subscribeMockScenario, getMockScenario)
  const handleChange = (value: string) => {
    const next = MOCK_SCENARIOS.find((candidate) => candidate === value)
    if (next) {
      setMockScenario(next)
    }
  }
  return (
    <Select value={scenario} onValueChange={handleChange}>
      <SelectTrigger aria-label={t('common.scenario.label')} size="sm">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {MOCK_SCENARIOS.map((candidate) => (
          <SelectItem key={candidate} value={candidate}>
            {t(`common.scenario.${candidate}`)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
