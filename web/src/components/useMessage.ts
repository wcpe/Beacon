// 写操作反馈的公共 hook，避免各页面重复实现。

import { useCallback, useState } from 'react'
import type { Message } from './MessageBar'

export interface MessageApi {
  message: Message | null
  showSuccess: (text: string) => void
  showError: (text: string) => void
  clear: () => void
}

export function useMessage(): MessageApi {
  const [message, setMessage] = useState<Message | null>(null)

  const showSuccess = useCallback((text: string) => setMessage({ kind: 'success', text }), [])
  const showError = useCallback((text: string) => setMessage({ kind: 'error', text }), [])
  const clear = useCallback(() => setMessage(null), [])

  return { message, showSuccess, showError, clear }
}
