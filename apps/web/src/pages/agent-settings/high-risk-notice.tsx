// 高危配置警告卡（/agent-settings）：命令白名单是「控制面 → 被管理端」唯一能执行命令的闸门。
//
// 为什么必须在页面上显式喊出来：ADR-0011 原本把白名单列为「不由控制面下发」，理由是
// 「可下发 = 管理面被攻破即无鉴权 RCE」。FR-277 按用户决策改为可下发（面板仅内网可达），
// 并以「改该项走审批 + 审计 + 界面高危警告」作为补偿措施——本组件就是那条补偿措施。
import { useTranslation } from 'react-i18next'

import { TriangleAlert } from 'lucide-react'

import { Badge } from '@beacon/ui'

export default function HighRiskNotice({ keys }: { keys: string[] }) {
  const { t } = useTranslation()

  return (
    <div className="grid gap-1.5 rounded-xl border border-warn-bd bg-warn-bg px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <TriangleAlert className="size-4 shrink-0 text-warn" aria-hidden />
        <h2 className="text-[13px] font-semibold text-warn">{t('system.agentSettings.highRisk.title')}</h2>
        <Badge variant="warn">{t('system.agentSettings.highRisk.badge')}</Badge>
        {keys.map((key) => (
          <span key={key} className="font-mono text-xs text-warn/80">
            {key}
          </span>
        ))}
      </div>
      <p className="text-xs leading-relaxed text-ink-2">{t('system.agentSettings.highRisk.body')}</p>
      <p className="text-xs text-ink-3">{t('system.agentSettings.highRisk.jsonHint')}</p>
    </div>
  )
}
