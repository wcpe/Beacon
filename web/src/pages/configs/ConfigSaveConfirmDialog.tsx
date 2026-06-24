// 在页编辑保存确认对话框（FR-67）：单文件在页编辑后点保存不直接发布，
// 先弹本对话框——展示配置三元组(ns/group/dataId) + Monaco DiffEditor（上一保存版本 ⟷ 当前编辑态）
// + 备注输入；确认才调既有保存（publishConfig），取消不发布。

import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'

import { impactPreview } from '../../api/client'
import CodeEditor from '../../components/CodeEditor'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

export default function ConfigSaveConfirmDialog({
  open,
  namespace,
  group,
  dataId,
  scopeLevel,
  scopeTarget,
  format,
  originalContent,
  currentContent,
  comment,
  pending,
  onCommentChange,
  onConfirm,
  onCancel,
}: {
  // 对话框开合
  open: boolean
  // 配置三元组（只展示）
  namespace: string
  group: string
  dataId: string
  // 该配置的覆盖层与目标（用于算发布影响面，FR-79）
  scopeLevel: string
  scopeTarget: string
  // diff 语言（按格式高亮）
  format: string
  // 上一已保存版本内容（diff 左侧）
  originalContent: string
  // 当前编辑态内容（diff 右侧 + 实际发布内容）
  currentContent: string
  // 备注（受控，回写到上层供发布时携带）
  comment: string
  // 确认发布进行中（禁用按钮）
  pending: boolean
  // 备注变更
  onCommentChange: (comment: string) => void
  // 确认 → 上层调 publishConfig
  onConfirm: () => void
  // 取消（不发布）
  onCancel: () => void
}) {
  const { t } = useTranslation()
  // 本地备注输入态：打开时以传入 comment 初始化，确认前回写上层。
  const [localComment, setLocalComment] = useState(comment)

  useEffect(() => {
    if (open) setLocalComment(comment)
  }, [open, comment])

  // 当前内容与上一版本是否一致（无变化时给提示，diff 仍展示）。
  const unchanged = originalContent === currentContent

  // 发布影响面（FR-79）：对话框打开时按该配置 scope 拉受影响在线子服集合，纯展示、不阻断发布。
  const impactQuery = useQuery({
    queryKey: ['config-impact', namespace, scopeLevel, group, scopeTarget],
    queryFn: () => impactPreview({ namespace, scopeLevel, group, scopeTarget }),
    enabled: open && !!namespace && !!scopeLevel,
  })

  function handleConfirm() {
    onCommentChange(localComment)
    onConfirm()
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) onCancel() }}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('configs.saveConfirmTitle')}</DialogTitle>
        </DialogHeader>

        {/* 配置三元组 + diff 提示 */}
        <div className="space-y-1 text-xs text-muted-foreground">
          <div className="font-mono">
            {t('configs.saveConfirmConfig', { namespace, group: group || '—', dataId })}
          </div>
          <div>
            {unchanged ? t('configs.saveConfirmNoChange') : t('configs.saveConfirmDiffHint')}
          </div>
          {/* 发布影响面（FR-79）：加载中 / 0 台 / N 台三态，纯展示 */}
          <div data-testid="save-confirm-impact">
            {impactQuery.isLoading
              ? t('configs.saveConfirmImpactLoading')
              : impactQuery.isError
                ? t('configs.saveConfirmImpactError')
                : impactQuery.data && impactQuery.data.total > 0
                  ? t('configs.saveConfirmImpactSummary', {
                      count: impactQuery.data.total,
                      servers: impactQuery.data.affected.join('、'),
                    })
                  : impactQuery.data
                    ? t('configs.saveConfirmImpactNone')
                    : null}
          </div>
        </div>

        {/* diff：左上一保存版本、右当前编辑态 */}
        <div className="h-72 rounded border border-border overflow-hidden">
          <CodeEditor
            original={originalContent}
            modified={currentContent}
            language={format}
          />
        </div>

        {/* 备注输入 */}
        <div className="space-y-1.5">
          <Label htmlFor="save-confirm-comment">{t('configs.saveConfirmCommentLabel')}</Label>
          <Input
            id="save-confirm-comment"
            value={localComment}
            onChange={(e) => setLocalComment(e.target.value)}
            placeholder={t('configs.saveConfirmCommentPlaceholder')}
          />
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onCancel} disabled={pending}>
            {t('configs.saveConfirmCancel')}
          </Button>
          <Button type="button" onClick={handleConfirm} disabled={pending}>
            {pending ? t('configs.saving') : t('configs.saveConfirmSubmit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
