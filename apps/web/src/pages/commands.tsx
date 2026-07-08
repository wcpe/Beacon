// 命令观测页（/commands）：agent 命令双向生命周期与队列。
// KPI 概览 + 实时在途队列（pending / fetched）+ 命令历史（过滤 / 分页）；与 /audits 互跳（FR-157）。
import { useTranslation } from 'react-i18next'
import { TerminalSquare } from 'lucide-react'

import { SectionHeader } from '@beacon/ui'

import CommandHistory from './commands/command-history'
import CommandKpi from './commands/command-kpi'
import CommandQueue from './commands/command-queue'

export default function CommandsPage() {
  const { t } = useTranslation()
  return (
    <section className="grid gap-5">
      <SectionHeader
        size="lg"
        icon={<TerminalSquare className="size-5" />}
        title={t('nav.commands')}
        count={t('observability.commands.mission')}
      />
      <CommandKpi />
      <CommandQueue />
      <CommandHistory />
    </section>
  )
}
