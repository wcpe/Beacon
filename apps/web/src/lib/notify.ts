// 全局写操作反馈：统一 sonner toast，避免页内横幅挤布局、模态 description 叠字。
// 依赖根节点挂载 <Toaster />（见 main.tsx）。

import { toast } from 'sonner'

export function notifySuccess(text: string): void {
  toast.success(text)
}

export function notifyError(text: string): void {
  toast.error(text)
}

/** 写操作成功 + 一个动作按钮（如「去审批中心」）：票据类操作的成功反馈都带下一步入口。 */
export function notifySuccessAction(text: string, action: { label: string; onClick: () => void }): void {
  toast.success(text, { action })
}
