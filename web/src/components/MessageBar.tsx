// 操作反馈条：展示写操作的成功/失败提示，可手动关闭。

export interface Message {
  kind: 'success' | 'error'
  text: string
}

export default function MessageBar({
  message,
  onClose,
}: {
  message: Message | null
  onClose: () => void
}) {
  if (!message) return null
  return (
    <div className={message.kind === 'success' ? 'msgbar msgbar-success' : 'msgbar msgbar-error'}>
      <span>{message.text}</span>
      <button type="button" className="msgbar-close" onClick={onClose} aria-label="关闭">
        ×
      </button>
    </div>
  )
}
