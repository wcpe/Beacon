// 编辑文件元数据弹窗（PATCH /admin/v2/config-files/{id}）：描述 / JSON Schema / 敏感键路径。
// 变更敏感路径属高风险操作（规格 §4.7）：提交前链入原因确认弹窗（原因必填）后才发 PATCH；
// 仅提交发生变化的字段，错误内联脱敏展示（ADR-0057）。
import { useState } from 'react'
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
import type { ConfigFileDetail } from '@beacon/contracts'

import { ApiClientError } from '../../api/delivery'
import { updateConfigFile, type UpdateConfigFileBody } from '../../api/delivery-configs'
import ReasonDialog from './reason-dialog'

interface MetadataDialogProps {
  file: ConfigFileDetail
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}

export default function MetadataDialog({ file, onOpenChange, onSaved }: MetadataDialogProps) {
  const { t } = useTranslation()
  const [description, setDescription] = useState(file.description)
  const [schemaText, setSchemaText] = useState(file.schemaJson ?? '')
  // 敏感路径以「每行一个」文本编辑
  const [sensitiveText, setSensitiveText] = useState(file.sensitivePaths.join('\n'))
  const [saveError, setSaveError] = useState<string | null>(null)
  // 敏感路径有变化时的高风险原因确认
  const [reasonOpen, setReasonOpen] = useState(false)

  const nextSensitivePaths = sensitiveText
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')
  const sensitiveChanged = nextSensitivePaths.join('\n') !== file.sensitivePaths.join('\n')

  const saveMutation = useMutation({
    mutationFn: (body: UpdateConfigFileBody) => updateConfigFile(file.id, body),
    onSuccess: () => {
      setReasonOpen(false)
      onSaved()
    },
    onError: (error) => {
      setReasonOpen(false)
      setSaveError(messageOf(error))
    },
  })

  // 组装仅含变化字段的 PATCH 体（精准修改；schema 清空以空串表达）
  const buildBody = (reason?: string): UpdateConfigFileBody => {
    const body: UpdateConfigFileBody = {}
    if (description !== file.description) {
      body.description = description
    }
    if (schemaText.trim() !== (file.schemaJson ?? '')) {
      body.schemaJson = schemaText.trim()
    }
    if (sensitiveChanged) {
      body.sensitivePaths = nextSensitivePaths
      body.reason = reason
    }
    return body
  }

  const hasChange = Object.keys(buildBody('_probe')).length > 0

  const submit = (): void => {
    setSaveError(null)
    if (sensitiveChanged) {
      // 高风险：改敏感路径必须给原因，二次确认后提交
      setReasonOpen(true)
      return
    }
    saveMutation.mutate(buildBody())
  }

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
          <DialogTitle className="text-ink-1">{t('delivery.configs.detail.metadata.title')}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="text-sm text-ink-3">
            <span className="font-mono text-ink-1">{file.name}</span>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-meta-desc">{t('delivery.configs.detail.metadata.descLabel')}</Label>
            <Textarea
              id="config-meta-desc"
              value={description}
              onChange={(e) => {
                setDescription(e.target.value)
              }}
              rows={2}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-meta-schema">{t('delivery.configs.detail.metadata.schemaLabel')}</Label>
            <Textarea
              id="config-meta-schema"
              value={schemaText}
              onChange={(e) => {
                setSchemaText(e.target.value)
              }}
              rows={6}
              className="font-mono text-xs"
              placeholder='{"type":"object","properties":{...}}'
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-meta-sensitive">{t('delivery.configs.detail.metadata.sensitiveLabel')}</Label>
            <Textarea
              id="config-meta-sensitive"
              value={sensitiveText}
              onChange={(e) => {
                setSensitiveText(e.target.value)
              }}
              rows={3}
              className="font-mono text-xs"
              placeholder="database.password"
            />
            {sensitiveChanged && (
              <p className="text-xs text-warn">{t('delivery.configs.detail.metadata.sensitiveChangedHint')}</p>
            )}
          </div>
          {saveError !== null && <p className="text-sm text-crit">{saveError}</p>}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('delivery.configs.detail.metadata.cancel')}
          </Button>
          <Button disabled={!hasChange || saveMutation.isPending} onClick={submit}>
            {t('delivery.configs.detail.metadata.save')}
          </Button>
        </DialogFooter>
      </DialogContent>

      {reasonOpen && (
        <ReasonDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setReasonOpen(false)
            }
          }}
          title={t('delivery.configs.detail.metadata.sensitiveConfirmTitle')}
          description={t('delivery.configs.detail.metadata.sensitiveConfirmDesc')}
          confirmLabel={t('delivery.configs.detail.metadata.sensitiveConfirm')}
          impacts={[
            `${t('delivery.configs.detail.metadata.sensitiveBefore')}：${file.sensitivePaths.join('、') || '-'}`,
            `${t('delivery.configs.detail.metadata.sensitiveAfter')}：${nextSensitivePaths.join('、') || '-'}`,
          ]}
          pending={saveMutation.isPending}
          errorText={null}
          onConfirm={(reason) => {
            saveMutation.mutate(buildBody(reason))
          }}
        />
      )}
    </Dialog>
  )
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}
