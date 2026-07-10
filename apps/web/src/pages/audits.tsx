// 审计页（/audits）：主从布局——KPI + 主列（吸顶筛选 + 自区滚列表 + 分页），右侧非模态详情面板。
// KPI + 过滤列表（操作人/动作/目标类型/关键词）+ 分页 + 右侧详情面板（追溯明细）+ 导出；与 /commands 互跳（FR-157）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ScrollText } from 'lucide-react'

import { SectionHeader } from '@beacon/ui'
import type { AuditItem } from '@beacon/contracts'

import MasterDetail from '../features/shared/master-detail'
import AuditDetailPanel from './audits/audit-detail-panel'
import AuditKpi from './audits/audit-kpi'
import AuditList from './audits/audit-list'

export default function AuditsPage() {
  const { t } = useTranslation()
  // 当前查看详情的审计行（null 表示右侧详情列收起）
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
      <MasterDetail
        master={<AuditList onView={setDetail} selectedId={detail?.id ?? null} />}
        detail={detail ? <AuditDetailPanel item={detail} /> : null}
        detailTitle={t('observability.audits.detailTitle')}
        closeLabel={t('observability.common.close')}
        onClose={() => {
          setDetail(null)
        }}
      />
    </section>
  )
}
