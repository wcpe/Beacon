// 编辑配置层弹窗：Textarea 编辑内容 + 备注，「语法校验」（只读）+「保存新版本」。
// 保存传 basedOnVersionId=该层当前 head versionId（链空传 null），语法/冲突/无变化错误内联脱敏。
import { useEffect, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Label,
  Textarea,
} from '@beacon/ui'
import type { ConfigScopeLevel } from '@beacon/devmock'

import { ApiClientError } from '../../api/delivery'
import { saveConfigVersion, validateConfig } from '../../api/delivery-configs'

export interface EditTarget {
  scopeLevel: ConfigScopeLevel
  scopeRefId: number
  scopeName: string
  // 该层当前 head 的 versionId；链空为 null
  headVersionId: number | null
  // 初始内容（脱敏后的 head 内容，敏感占位符原样保留由后端回填）
  initialContent: string
}

interface EditDialogProps {
  fileId: number
  target: EditTarget
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}

export default function EditDialog({ fileId, target, onOpenChange, onSaved }: EditDialogProps) {
  const { t } = useTranslation()
  const [content, setContent] = useState(target.initialContent)
  const [remark, setRemark] = useState('')
  const [validateText, setValidateText] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)

  // 目标切换时重置草稿
  useEffect(() => {
    setContent(target.initialContent)
    setRemark('')
    setValidateText(null)
    setSaveError(null)
  }, [target])

  const validateMutation = useMutation({
    mutationFn: () => validateConfig(fileId, { scopeLevel: target.scopeLevel, content }),
    onSuccess: (result) => {
      setValidateText(
        result.valid
          ? t('delivery.configs.detail.edit.validateOk')
          : `${t('delivery.configs.detail.edit.validateFail')}：${result.errors
              .map((e) => `${e.path} ${e.message}`)
              .join('; ')}`,
      )
    },
    onError: (error) => {
      setValidateText(messageOf(error))
    },
  })

  const saveMutation = useMutation({
    mutationFn: () =>
      saveConfigVersion(fileId, {
        scopeLevel: target.scopeLevel,
        scopeRefId: target.scopeRefId,
        content,
        remark: remark.trim() === '' ? undefined : remark.trim(),
        basedOnVersionId: target.headVersionId,
      }),
    onSuccess: () => {
      onSaved()
    },
    onError: (error) => {
      setSaveError(
        error instanceof ApiClientError && error.code === 'CONFIG_VERSION_CONFLICT'
          ? t('delivery.configs.detail.edit.conflict')
          : messageOf(error),
      )
    },
  })

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) {
          onOpenChange(false)
        }
      }}
    >
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('delivery.configs.detail.edit.title')}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="text-sm text-muted-foreground">
            {t('delivery.configs.detail.edit.scopeLabel')}：
            <span className="font-mono">
              {target.scopeLevel} / {target.scopeName}
            </span>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-edit-content">{t('delivery.configs.detail.edit.contentLabel')}</Label>
            <Textarea
              id="config-edit-content"
              value={content}
              onChange={(e) => {
                setContent(e.target.value)
                setValidateText(null)
                setSaveError(null)
              }}
              rows={12}
              className="font-mono text-xs"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-edit-remark">{t('delivery.configs.detail.edit.remarkLabel')}</Label>
            <Textarea
              id="config-edit-remark"
              value={remark}
              onChange={(e) => {
                setRemark(e.target.value)
              }}
              rows={2}
            />
          </div>
          {validateText !== null && <p className="text-sm text-muted-foreground">{validateText}</p>}
          {saveError !== null && <p className="text-sm text-destructive">{saveError}</p>}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('delivery.configs.detail.edit.cancel')}
          </Button>
          <Button
            variant="outline"
            disabled={validateMutation.isPending}
            onClick={() => {
              validateMutation.mutate()
            }}
          >
            {t('delivery.configs.detail.edit.validate')}
          </Button>
          <Button
            disabled={saveMutation.isPending}
            onClick={() => {
              setSaveError(null)
              saveMutation.mutate()
            }}
          >
            {t('delivery.configs.detail.edit.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}
