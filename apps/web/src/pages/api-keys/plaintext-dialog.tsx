// 一次性明文展示弹窗：密钥创建 / 重置后仅此一次显示明文，提供复制。
// 明文属敏感瞬态，仅当前会话内存持有，关闭即弃。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@beacon/ui'

interface PlaintextDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  // 一次性明文（密钥或接入 token）
  plaintext: string
}

export default function PlaintextDialog({ open, onOpenChange, title, plaintext }: PlaintextDialogProps) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)

  const copy = () => {
    // clipboard 在非安全上下文 / 测试环境可能缺失，DOM 类型标注为必有、运行期未必，故先经 unknown 再判空
    const clipboard = (navigator.clipboard as unknown) as Clipboard | undefined
    if (clipboard === undefined) {
      return
    }
    // 失败静默降级为手动选中展示，不阻断
    void clipboard.writeText(plaintext).then(
      () => {
        setCopied(true)
      },
      () => {
        // 复制失败不阻断，用户仍可手动选中明文
      },
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          setCopied(false)
        }
        onOpenChange(next)
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{t('system.apiKeys.plaintextDesc')}</DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-3 py-2 font-mono text-sm">
            {plaintext}
          </code>
          <Button size="sm" variant="outline" onClick={copy}>
            {copied ? t('system.apiKeys.copied') : t('system.apiKeys.copy')}
          </Button>
        </div>
        <DialogFooter>
          <Button
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('system.apiKeys.plaintextClose')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
