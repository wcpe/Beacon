// 写操作反馈与操作人校验的公共 hook，避免各页面重复实现。

import { useCallback, useState } from 'react'
import type { Message } from './MessageBar'

export interface MessageApi {
  message: Message | null
  showSuccess: (text: string) => void
  showError: (text: string) => void
  clear: () => void
  // 校验操作人非空：为空时给出错误提示并返回 false
  requireOperator: (operator: string) => boolean
}

export function useMessage(): MessageApi {
  const [message, setMessage] = useState<Message | null>(null)

  const showSuccess = useCallback((text: string) => setMessage({ kind: 'success', text }), [])
  const showError = useCallback((text: string) => setMessage({ kind: 'error', text }), [])
  const clear = useCallback(() => setMessage(null), [])

  const requireOperator = useCallback(
    (operator: string): boolean => {
      if (operator.trim()) return true
      setMessage({ kind: 'error', text: '请先在左侧填写「操作人」后再执行此操作' })
      return false
    },
    [],
  )

  return { message, showSuccess, showError, clear, requireOperator }
}
