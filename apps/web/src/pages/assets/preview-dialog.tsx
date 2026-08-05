import DemoSensitiveAccessDialog from '../../features/approval/demo-sensitive-access-dialog'
import { assetReadCommandId, consumeAssetPreviewGrant, requestAssetPreviewApproval } from '../../api/delivery-assets'

interface PreviewTarget {
  serverId: string
  path: string
}

interface PreviewDialogProps {
  target: PreviewTarget | null
  onOpenChange: (open: boolean) => void
}

export default function PreviewDialog({ target, onOpenChange }: PreviewDialogProps) {
  if (target === null) return null
  const resourceId = `${target.serverId}:${target.path}`
  return (
    <DemoSensitiveAccessDialog
      open
      onOpenChange={onOpenChange}
      mode="real"
      targetRef={resourceId}
      createApproval={(reason, idempotencyKey) => requestAssetPreviewApproval(target.serverId, target.path, reason, idempotencyKey)}
      consumeApproval={async (approval) => {
        const grantId = approval.sensitiveAccessGrant?.grantId
        const commandId = assetReadCommandId(approval.resultRef)
        if (!grantId || commandId === null) throw new Error('审批结果未提供可消费授权')
        await consumeAssetPreviewGrant(grantId, commandId)
      }}
    />
  )
}
