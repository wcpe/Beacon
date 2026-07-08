// 审计页（/audits）：审计查询、追溯与导出。
// KPI + 过滤列表（操作人/动作/目标类型/关键词）+ 分页 + 行详情抽屉 + 导出；与 /commands 互跳（FR-157）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ScrollText } from 'lucide-react'

import { SectionHeader } from '@beacon/ui'
import type { AuditItem } from '@beacon/devmock'

import AuditDetailSheet from './audits/audit-detail-sheet'
import AuditKpi from './audits/audit-kpi'
import AuditList from './audits/audit-list'

export default function AuditsPage() {
  const { t } = useTranslation()
  // 当前查看详情的审计行
  const [detail, setDetail] = useState<AuditItem | null>(null)

  return (
    <section className="grid gap-5">
      <SectionHeader
        size="lg"
        icon={<ScrollText className="size-5" />}
        title={t('nav.audits')}
        count={t('observability.audits.mission')}
      />
      <AuditKpi />
      <AuditList onView={setDetail} />
      <AuditDetailSheet
        item={detail}
        onOpenChange={(open) => {
          if (!open) {
            setDetail(null)
          }
        }}
      />
    </section>
  )
}
