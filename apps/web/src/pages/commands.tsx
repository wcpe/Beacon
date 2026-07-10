// 命令观测页（/commands）：主从布局——KPI 吸顶 + 主列（在途队列 + 命令历史，筛选吸顶、列表自区滚），
// 右侧非模态详情面板看命令双向生命周期。与 /audits 互跳（FR-157）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { TerminalSquare } from 'lucide-react'

import { SectionHeader } from '@beacon/ui'
import type { CommandItem } from '@beacon/contracts'

import MasterDetail from '../features/shared/master-detail'
import CommandDetailPanel from './commands/command-detail-panel'
import CommandHistory from './commands/command-history'
import CommandKpi from './commands/command-kpi'
import CommandQueue from './commands/command-queue'

export default function CommandsPage() {
  const { t } = useTranslation()
  // 当前查看详情的命令行（null 表示右侧详情列收起）
  const [detail, setDetail] = useState<CommandItem | null>(null)
  const selectedId = detail?.commandId ?? null

  return (
    <section className="grid gap-5">
      <SectionHeader
        size="lg"
        icon={<TerminalSquare className="size-5" />}
        title={t('nav.commands')}
        count={t('observability.commands.mission')}
      />
      <CommandKpi />
      <MasterDetail
        master={
          <div className="grid gap-5">
            <CommandQueue onView={setDetail} selectedId={selectedId} />
            <CommandHistory onView={setDetail} selectedId={selectedId} />
          </div>
        }
        detail={detail ? <CommandDetailPanel item={detail} /> : null}
        detailTitle={t('observability.commands.detailTitle')}
        closeLabel={t('observability.common.close')}
        onClose={() => {
          setDetail(null)
        }}
      />
    </section>
  )
}
