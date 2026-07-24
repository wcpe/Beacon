// 敏感路径规则编辑弹窗（GET/PUT /admin/v2/assets/sensitive-rules，FR-164）：
// 每行一个 glob，整体替换语义；命中的文件预览 / diff 默认禁止查看内容、需填原因单次放行（记入审计）。清空 = 关闭保护。
// 属 /assets 页内非结构性小面板（ux-spec §3 小改豁免），错误内联脱敏展示（ADR-0057）。
import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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
  Textarea,
} from '@beacon/ui'

import { ApiClientError } from '../../api/delivery'
import { fetchSensitiveRules, updateSensitiveRules } from '../../api/delivery-assets'

interface SensitiveRulesDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export default function SensitiveRulesDialog({ open, onOpenChange }: SensitiveRulesDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [text, setText] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const rulesQuery = useQuery({
    queryKey: ['assets', 'sensitive-rules'],
    queryFn: fetchSensitiveRules,
    enabled: open,
  })

  // 数据到达后初始化文本一次；关闭后复位（下次打开重新拉取初始化）。
  useEffect(() => {
    if (rulesQuery.data && !loaded) {
      setText(rulesQuery.data.patterns.join('\n'))
      setLoaded(true)
    }
  }, [rulesQuery.data, loaded])
  useEffect(() => {
    if (!open) {
      setLoaded(false)
      setSaved(false)
      setSaveError(null)
    }
  }, [open])

  const nextPatterns = text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')

  const saveMutation = useMutation({
    mutationFn: (patterns: string[]) => updateSensitiveRules(patterns),
    onSuccess: (data) => {
      setText(data.patterns.join('\n'))
      setSaveError(null)
      setSaved(true)
      void queryClient.invalidateQueries({ queryKey: ['assets', 'sensitive-rules'] })
    },
    onError: (error) => {
      setSaved(false)
      setSaveError(error instanceof ApiClientError ? error.message : String(error))
    },
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          onOpenChange(false)
        }
      }}
    >
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="text-ink-1">{t('delivery.assets.sensitiveRules.title')}</DialogTitle>
          <DialogDescription>{t('delivery.assets.sensitiveRules.hint')}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="asset-sensitive-rules">{t('delivery.assets.sensitiveRules.label')}</Label>
            <Textarea
              id="asset-sensitive-rules"
              aria-label={t('delivery.assets.sensitiveRules.label')}
              value={text}
              onChange={(e) => {
                setText(e.target.value)
                setSaved(false)
              }}
              rows={10}
              className="font-mono text-xs"
              placeholder={t('delivery.assets.sensitiveRules.placeholder')}
              disabled={rulesQuery.isLoading}
            />
            {!rulesQuery.isLoading && nextPatterns.length === 0 && (
              <p className="text-xs text-warn">{t('delivery.assets.sensitiveRules.empty')}</p>
            )}
          </div>
          {rulesQuery.isError && (
            <p className="text-sm text-crit">{t('delivery.assets.sensitiveRules.loadError')}</p>
          )}
          {saveError !== null && <p className="text-sm text-crit">{saveError}</p>}
          {saved && <p className="text-sm text-ok">{t('delivery.assets.sensitiveRules.saved')}</p>}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('delivery.assets.sensitiveRules.cancel')}
          </Button>
          <Button
            disabled={rulesQuery.isLoading || saveMutation.isPending}
            onClick={() => {
              saveMutation.mutate(nextPatterns)
            }}
          >
            {t('delivery.assets.sensitiveRules.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
