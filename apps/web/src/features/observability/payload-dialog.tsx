import DemoSensitiveAccessDialog from '../approval/demo-sensitive-access-dialog'
import { consumeMessagePayloadGrant, requestMessagePayloadApproval } from '../../api/connections'

interface PayloadDialogProps {
  messageId: string
  onClose: () => void
}

export default function PayloadDialog({ messageId, onClose }: PayloadDialogProps) {
  return (
    <DemoSensitiveAccessDialog
      open
      onOpenChange={(open) => { if (!open) onClose() }}
      mode="real"
      targetRef={messageId}
      createApproval={(reason, idempotencyKey) => requestMessagePayloadApproval(messageId, reason, idempotencyKey)}
      consumeApproval={async (approval) => {
        const grantId = approval.sensitiveAccessGrant?.grantId
        if (!grantId) throw new Error('审批结果未提供可消费授权')
        await consumeMessagePayloadGrant(grantId, messageId)
      }}
    />
  )
}
