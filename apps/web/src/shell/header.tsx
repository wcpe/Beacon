// 页眉（对齐 B 版）：56px、半透明白 + backdrop-blur，固定不随内容滚动。
// 折叠触发器已移至侧栏右缘（见 app-shell）；页眉聚焦运行态与演示控制：
// 左侧控制面在线状态药丸；右侧常驻演示模式徽标（FR-159）+ 四态场景切换器。
import { useTranslation } from 'react-i18next'

import { Badge } from '@beacon/ui'

import ScenarioSwitcher from './scenario-switcher'

export default function Header() {
  const { t } = useTranslation()
  return (
    <header className="flex h-14 shrink-0 items-center gap-3.5 border-b border-border bg-background/85 px-[22px] backdrop-blur-md">
      {/* 左侧：控制面在线状态药丸（绿底 + 呼吸圆点） */}
      <Badge variant="ok" className="gap-1.5">
        <span className="size-[7px] rounded-full bg-current shadow-[0_0_0_3px_color-mix(in_srgb,currentColor_18%,transparent)]" />
        {t('common.controlPlaneOnline')}
      </Badge>
      {/* 右侧：演示模式徽标 + 四态场景切换器 */}
      <div className="flex flex-1 items-center justify-end gap-2.5">
        <Badge variant="secondary">{t('common.demoMode')}</Badge>
        <ScenarioSwitcher />
      </div>
    </header>
  )
}
