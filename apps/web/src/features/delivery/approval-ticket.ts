// 交付申请票据的统一反馈（submit / delete / resume / confirm / rollback / rollback-finish 共用）：
// 成功提示「已提交审批（申请号 X）」+ 一个「去审批中心」动作按钮。
// 复用全站既有写操作反馈（sonner toast，见 lib/notify），不引入新模态；
// 票据只说明申请已受理，单据状态要到审批通过后才变化，故不给「已完成」类文案。
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'

import type { DeliveryApprovalTicket } from '../../api/delivery-changes'
import { notifySuccessAction } from '../../lib/notify'

/** 返回票据成功反馈回调：展示申请号，并可一键跳转到该申请的审批详情。 */
export function useApprovalTicketFeedback(): (ticket: DeliveryApprovalTicket) => void {
  const { t } = useTranslation()
  const navigate = useNavigate()
  return (ticket) => {
    notifySuccessAction(t('delivery.changes.ticket.submitted', { id: ticket.approvalRequestId }), {
      label: t('delivery.changes.ticket.open'),
      onClick: () => {
        void navigate(`/approvals/${encodeURIComponent(ticket.approvalRequestId)}`)
      },
    })
  }
}
