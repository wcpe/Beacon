// 全局写操作反馈：统一 sonner toast，避免页内横幅挤布局、模态 description 叠字。
// 依赖根节点挂载 <Toaster />（见 main.tsx）。

import { toast } from 'sonner'

export function notifySuccess(text: string): void {
  toast.success(text)
}

export function notifyError(text: string): void {
  toast.error(text)
}
