// 回滚高风险确认弹窗（共享控件）：手输复述词 + 原因必填（承 UX §4 高风险二次确认 + 原因）。
// 手输不等于复述词则禁用确认；提交进行中禁用；内联脱敏错误。/changes 与历史页共用。
//
// 三处复用同一控件、只换文案与附加提示：
//   - 整单回滚（FR-167）：复述「回滚」；
//   - 目标级子集回滚（FR-270）：复述「回滚」+ 明示「只回滚选中台的文件、配置版本不回退」；
//   - 回滚重试（FR-262r）：复述「重试」+ 明示「只重推失败目标」。
//
// 回滚预检（FR-255r）作为可选区块嵌在弹窗内：动手前先说明哪些台**根本回不了**，
// 且预检读不到数据时显示「预检不可用」而不是显示 0 台——不把未知伪装成安全。
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  Input,
  Label,
  Textarea,
} from '@beacon/ui'

/** 回滚预检结果：unavailable = 拿不到目标备份状态（未知），区别于「全部可回滚」 */
export interface RollbackPrecheck {
  unavailable: boolean
  /** 因备份缺失而无法文件回滚的 serverId */
  blocked: string[]
  /** 已检查的目标数（预检可能只覆盖前 N 台） */
  checked: number
  total: number
}

interface RollbackDialogProps {
  open: boolean
  pending: boolean
  errorText: string | null
  onConfirm: (reason: string) => void
  onOpenChange: (open: boolean) => void
  /** 文案路径覆盖：默认整单回滚的既有文案（不传即行为逐字不变） */
  textKey?: 'rollback' | 'rollback.targets' | 'rollback.retry'
  /** 复述词覆盖：默认取 textKey 下的 phrase */
  phrase?: string
  /** 目标级回滚的台数与全选提示（FR-270） */
  selectedCount?: number
  allSelected?: boolean
  /** 回滚预检（FR-255r）：不传即不渲染预检区块 */
  precheck?: RollbackPrecheck | null
}

export default function RollbackDialog({
  open,
  pending,
  errorText,
  onConfirm,
  onOpenChange,
  textKey = 'rollback',
  phrase: phraseOverride,
  selectedCount,
  allSelected = false,
  precheck,
}: RollbackDialogProps) {
  const { t } = useTranslation()
  const base = `delivery.${textKey}`
  const phrase = phraseOverride ?? t(`${base}.phrase`)
  const [typed, setTyped] = useState('')
  const [reason, setReason] = useState('')

  // 每次打开清空草稿
  useEffect(() => {
    if (open) {
      setTyped('')
      setReason('')
    }
  }, [open])

  const canConfirm = typed.trim() === phrase && reason.trim() !== '' && !pending

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(`${base}.title`)}</AlertDialogTitle>
          <AlertDialogDescription>{t(`${base}.desc`)}</AlertDialogDescription>
        </AlertDialogHeader>

        {/* 回滚预检（FR-255r）：先说明哪些台不可文件回滚，再让人填原因 */}
        {precheck != null && (
          <div className="rounded-md border border-warn-bd bg-warn-bg px-3 py-2 text-xs text-warn">
            <p className="font-medium">{t('delivery.rollback.precheck.title')}</p>
            {precheck.unavailable ? (
              <p>{t('delivery.rollback.precheck.unavailable')}</p>
            ) : precheck.blocked.length === 0 ? (
              <p>{t('delivery.rollback.precheck.allRollbackable')}</p>
            ) : (
              <>
                <p>{t('delivery.rollback.precheck.blocked', { count: precheck.blocked.length })}</p>
                <p className="font-mono break-all">
                  {t('delivery.rollback.precheck.blockedList', { serverIds: precheck.blocked.join('、') })}
                </p>
              </>
            )}
            {!precheck.unavailable && precheck.total > precheck.checked && (
              <p>
                {t('delivery.changes.detail.rollbackRecords.partialPrecheck', {
                  total: precheck.total,
                  checked: precheck.checked,
                })}
              </p>
            )}
          </div>
        )}

        {/* 目标级回滚的范围与「配置未回退」提示（FR-270）：不可忽略，不折叠 */}
        {textKey === 'rollback.targets' && (
          <div className="rounded-md border border-warn-bd bg-warn-bg px-3 py-2 text-xs text-warn">
            {typeof selectedCount === 'number' && (
              <p className="font-medium">
                {t('delivery.rollback.targets.selectedCount', { count: selectedCount })}
              </p>
            )}
            <p>{allSelected ? t('delivery.rollback.targets.allSelectedNotice') : t('delivery.rollback.targets.configNotice')}</p>
          </div>
        )}

        <div className="space-y-1.5">
          <Label htmlFor="rollback-phrase">{t(`${base}.phraseLabel`, { phrase })}</Label>
          <Input
            id="rollback-phrase"
            aria-label={t(`${base}.phraseLabel`, { phrase })}
            value={typed}
            onChange={(e) => {
              setTyped(e.target.value)
            }}
          />
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="rollback-reason">{t(`${base}.reasonLabel`)}</Label>
          <Textarea
            id="rollback-reason"
            aria-label={t(`${base}.reasonLabel`)}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value)
            }}
            placeholder={t(`${base}.reasonPlaceholder`)}
            rows={2}
          />
        </div>

        {errorText && <p className="text-sm text-destructive">{errorText}</p>}

        <AlertDialogFooter>
          <AlertDialogCancel>{t('delivery.changes.create.cancel')}</AlertDialogCancel>
          <AlertDialogAction
            disabled={!canConfirm}
            onClick={(e) => {
              e.preventDefault()
              onConfirm(reason.trim())
            }}
          >
            {t(`${base}.confirm`)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
