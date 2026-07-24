// 一次性接入 token 展示弹窗：namespace 创建后仅此一次显示明文 token，提供复制。
// token 属敏感瞬态，仅当前会话内存持有，关闭即弃。
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
  Label,
} from '@beacon/ui'

interface TokenDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // 一次性明文接入 token
  token: string
}

export default function TokenDialog({ open, onOpenChange, token }: TokenDialogProps) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)

  const copy = () => {
    // clipboard 在非安全上下文 / 测试环境可能缺失，DOM 类型标注为必有、运行期未必，故先经 unknown 再判空
    const clipboard = navigator.clipboard as unknown as Clipboard | undefined
    if (clipboard === undefined) {
      return
    }
    void clipboard.writeText(token).then(
      () => {
        setCopied(true)
      },
      () => {
        // 复制失败不阻断，用户仍可手动选中 token
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
          <DialogTitle>{t('system.namespaces.tokenTitle')}</DialogTitle>
          <DialogDescription>{t('system.namespaces.tokenDesc')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label>{t('system.namespaces.tokenLabel')}</Label>
          <div className="flex items-center gap-2">
            <code className="min-w-0 flex-1 truncate rounded-md bg-muted px-3 py-2 font-mono text-sm">
              {token}
            </code>
            <Button size="sm" variant="outline" onClick={copy}>
              {copied ? t('system.namespaces.copied') : t('system.namespaces.copy')}
            </Button>
          </div>
        </div>
        <DialogFooter>
          <Button
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('system.namespaces.tokenClose')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
