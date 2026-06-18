// 写操作反馈：统一走 sonner toast（成功/失败）。
// 保留 useMessage / showSuccess / showError 调用形态，各页无需感知底层从消息条换成了 toast。

import { toast } from 'sonner'

export interface MessageApi {
  showSuccess: (text: string) => void
  showError: (text: string) => void
}

export function useMessage(): MessageApi {
  return {
    showSuccess: (text: string) => toast.success(text),
    showError: (text: string) => toast.error(text),
  }
}
