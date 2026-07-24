// 编辑配置层弹窗：Textarea 编辑内容 + 备注，实时校验（内容变更 debounce 500ms 调 validate
// 端点，语法 / schema 违例逐条 {path,message} 内联展示）+ 手动「立即校验」按钮 +「保存新版本」。
// 保存传 basedOnVersionId=该层当前 head versionId（链空传 null），语法/schema/冲突/无变化错误内联脱敏。
// 文件含敏感键时显示占位符说明条：占位符保持不变即沿用旧值，替换新值保存后不可再查看明文（§4.7）。
import { useEffect, useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ShieldAlert } from 'lucide-react'

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
import type { ConfigScopeLevel, ConfigValidateResponse } from '@beacon/contracts'

import { ApiClientError } from '../../api/delivery'
import { saveConfigVersion, validateConfig } from '../../api/delivery-configs'

// 实时校验去抖间隔（毫秒）
const VALIDATE_DEBOUNCE_MS = 500

/** 敏感值占位符字面量（与后端契约一致，仅作提示展示） */
const MASKED_PLACEHOLDER = '__BEACON_MASKED__'

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
  // 文件级敏感键路径（非空时显示占位符说明条）
  sensitivePaths?: string[]
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}

export default function EditDialog({ fileId, target, sensitivePaths, onOpenChange, onSaved }: EditDialogProps) {
  const { t } = useTranslation()
  const [content, setContent] = useState(target.initialContent)
  const [remark, setRemark] = useState('')
  // 最近一次校验结果（实时与手动共用）；null = 尚未校验
  const [validation, setValidation] = useState<ConfigValidateResponse | null>(null)
  const [validateError, setValidateError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  // 跳过挂载 / 目标切换后的首次内容态（只在用户改动后触发实时校验）
  const skipNextValidate = useRef(true)

  // 目标切换时重置草稿
  useEffect(() => {
    setContent(target.initialContent)
    setRemark('')
    setValidation(null)
    setValidateError(null)
    setSaveError(null)
    skipNextValidate.current = true
  }, [target])

  const validateMutation = useMutation({
    mutationFn: (draft: string) => validateConfig(fileId, { scopeLevel: target.scopeLevel, content: draft }),
    onSuccess: (result) => {
      setValidateError(null)
      setValidation(result)
    },
    onError: (error) => {
      setValidation(null)
      setValidateError(messageOf(error))
    },
  })
  const { mutate: runValidate } = validateMutation

  // 实时校验：内容变更 500ms 去抖后调 validate 端点（不落库不审计）
  useEffect(() => {
    if (skipNextValidate.current) {
      skipNextValidate.current = false
      return
    }
    const timer = setTimeout(() => {
      runValidate(content)
    }, VALIDATE_DEBOUNCE_MS)
    return () => {
      clearTimeout(timer)
    }
  }, [content, runValidate])

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
          <DialogTitle className="text-ink-1">{t('delivery.configs.detail.edit.title')}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="text-sm text-ink-3">
            {t('delivery.configs.detail.edit.scopeLabel')}：
            <span className="font-mono text-ink-1">
              {target.scopeLevel} / {target.scopeName}
            </span>
          </div>
          {sensitivePaths !== undefined && sensitivePaths.length > 0 && (
            <div className="flex items-start gap-2 rounded-lg border border-warn-bd bg-warn-bg px-3 py-2 text-xs text-warn">
              <ShieldAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden />
              <span>
                {t('delivery.configs.detail.edit.sensitiveHint', {
                  paths: sensitivePaths.join('、'),
                  placeholder: MASKED_PLACEHOLDER,
                })}
              </span>
            </div>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="config-edit-content">{t('delivery.configs.detail.edit.contentLabel')}</Label>
            <Textarea
              id="config-edit-content"
              value={content}
              onChange={(e) => {
                setContent(e.target.value)
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

          {/* 校验结果区：进行中 / 通过 / 逐条违例（实时与手动共用一个出口） */}
          {validateMutation.isPending && (
            <p className="text-sm text-ink-3">{t('delivery.configs.detail.edit.validating')}</p>
          )}
          {!validateMutation.isPending && validation !== null && validation.valid && (
            <p className="text-sm text-ok">{t('delivery.configs.detail.edit.validateOk')}</p>
          )}
          {!validateMutation.isPending && validation !== null && !validation.valid && (
            <div className="grid gap-1 rounded-lg border border-crit-bd bg-crit-bg px-3 py-2">
              <span className="text-sm text-crit">{t('delivery.configs.detail.edit.validateFail')}</span>
              <ul className="grid gap-0.5">
                {validation.errors.map((issue, index) => (
                  <li key={`${issue.path}:${String(index)}`} className="font-mono text-xs text-crit">
                    {issue.path}：{issue.message}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {validateError !== null && <p className="text-sm text-crit">{validateError}</p>}
          {saveError !== null && <p className="text-sm text-crit">{saveError}</p>}
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
              runValidate(content)
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
