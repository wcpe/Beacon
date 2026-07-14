// 空层首次贡献的实体选择弹窗：为某作用域层级挑选具体实体（bc_cluster / region / zone / server），
// 已有贡献链的实体被排除（编辑走各层行内「编辑本层」）。选定后交由调用方打开空白编辑器。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Button,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Label,
} from '@beacon/ui'
import type { ConfigScopeLevel } from '@beacon/contracts'

import ScopeRefPicker, { type ScopeRef } from './scope-ref-picker'

interface AddScopeDialogProps {
  namespaceId: number
  level: Exclude<ConfigScopeLevel, 'namespace'>
  // 该层已有贡献链的实体 id（排除出候选）
  excludeIds: number[]
  onOpenChange: (open: boolean) => void
  onPicked: (ref: ScopeRef) => void
}

export default function AddScopeDialog({ namespaceId, level, excludeIds, onOpenChange, onPicked }: AddScopeDialogProps) {
  const { t } = useTranslation()
  const [picked, setPicked] = useState<ScopeRef | null>(null)

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) {
          onOpenChange(false)
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="text-ink-1">
            {t('delivery.configs.addScope.title', { level: t(`delivery.configs.detail.scopes.levels.${level}`) })}
          </DialogTitle>
        </DialogHeader>
        <div className="grid gap-2">
          <Label htmlFor="config-add-scope-ref">{t('delivery.configs.scopePicker.pickLabel')}</Label>
          <ScopeRefPicker
            id="config-add-scope-ref"
            namespaceId={namespaceId}
            level={level}
            value={picked}
            onChange={setPicked}
            excludeIds={excludeIds}
          />
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            {t('delivery.configs.addScope.cancel')}
          </Button>
          <Button
            disabled={picked === null}
            onClick={() => {
              if (picked !== null) {
                onPicked(picked)
              }
            }}
          >
            {t('delivery.configs.addScope.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
