// 工作台「新建受管配置」对话框（FR-111 接真后端）：在当前 scope 覆盖层下新建一份文件树对象（file_object）。
// 覆盖层由父级传入的 scope chip 推导（global / group:X / server:X），此处只读展示，不在表单里重选；
// 路径（如 plugins/Essentials/config.yml）必填，内容多行可空；提交调 createFile（POST /files）。
// 成功后由父级失效 wb-managed-tree 刷新左面板，并 toast。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'
import { Input } from '@beacon/ui'
import { Label } from '@beacon/ui'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@beacon/ui'

export default function WbCreateFileDialog({
  open,
  onOpenChange,
  // 覆盖层只读说明（如「全局」「组 main」「实例 lobby-1」），由父级按当前 scope chip 给出
  scopeLabel,
  // 父级提交中（createFile mutation pending）：禁用提交避免重复点击
  submitting,
  // 预填路径（右键 new 时可带当前文件夹前缀，留空则空表单）
  initialPath,
  // 提交：父级据此调 createFile（path 已去空白校验过）
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  scopeLabel: string
  submitting?: boolean
  initialPath?: string
  onSubmit: (path: string, content: string) => void
}) {
  const { t } = useTranslation()
  const [path, setPath] = useState('')
  const [content, setContent] = useState('')

  // 打开时重置表单（按 initialPath 预填）：每次唤起拿到干净表单
  useEffect(() => {
    if (open) {
      setPath(initialPath ?? '')
      setContent('')
    }
  }, [open, initialPath])

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    // 受管文件 path 约定为「相对 plugins/ 根」（受管树固定以 plugins 为根）：
    // 去掉用户可能误带的前导斜杠与 plugins/ 前缀，避免在树里出现「plugins/plugins/…」双层。
    const trimmed = path
      .trim()
      .replace(/^\/+/, '')
      .replace(/^plugins\//, '')
    if (!trimmed) return
    onSubmit(trimmed, content)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('configs.workbench.createFileTitle')}</DialogTitle>
        </DialogHeader>
        <form id="wb-create-file" onSubmit={onCreate} className="space-y-3">
          {/* 覆盖层只读说明：落点由当前 scope chip 决定，不在此重选 */}
          <p className="text-xs text-muted-foreground">
            {t('configs.workbench.createFileScopeHint', { scope: scopeLabel })}
          </p>
          <div className="space-y-1.5">
            <Label htmlFor="wb-cf-path">{t('configs.workbench.createFilePathLabel')}</Label>
            <Input
              id="wb-cf-path"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder={t('configs.workbench.createFilePathPlaceholder')}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="wb-cf-content">{t('configs.workbench.createFileContentLabel')}</Label>
            <textarea
              id="wb-cf-content"
              value={content}
              onChange={(e) => setContent(e.target.value)}
              rows={8}
              className="block w-full rounded border border-input bg-background px-2 py-1.5 font-mono text-xs"
              placeholder={t('configs.workbench.createFileContentPlaceholder')}
            />
          </div>
        </form>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
          >
            {t('common.cancel')}
          </Button>
          <Button type="submit" form="wb-create-file" disabled={submitting || !path.trim()}>
            {submitting
              ? t('configs.workbench.createFileSubmitting')
              : t('configs.workbench.createFileSubmit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
