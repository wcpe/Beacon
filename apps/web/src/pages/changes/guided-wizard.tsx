// 引导创建五步向导（模态 Dialog）：选交付内容 → 选模板源扫差异 → 选配置变更 →
// 范围与批次 → 影响预览与提交。步骤间状态保留；纯配置跳过模板源步、纯文件跳过配置步。
// 组合既有 mock 端点闭环：POST /change-orders（懒建 draft）→ diff-scan → PATCH（范围 /
// 批次 / 挂配置）→ impact → submit；取消时删除已建 draft，成单后交给父级打开详情面板。
import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Check } from 'lucide-react'

import { Button, Dialog, DialogContent, DialogHeader, DialogTitle, cn } from '@beacon/ui'

import { fetchZoneTree } from '../../api/cluster'
import { ApiClientError } from '../../api/delivery'
import {
  createChangeOrder,
  deleteChangeOrder,
  diffScanChangeOrder,
  submitChangeOrder,
  updateChangeOrder,
  type ChangeOrderDetail,
} from '../../api/delivery-changes'
import WizardStepContent from './wizard-step-content'
import WizardStepSource, { type ScanResult } from './wizard-step-source'
import WizardStepConfig from './wizard-step-config'
import WizardStepScope from './wizard-step-scope'
import WizardStepReview from './wizard-step-review'
import {
  WIZARD_STEPS,
  activeSteps,
  batchIssue,
  buildBatch,
  buildSelector,
  estimateTargetTotal,
  flattenZoneCounts,
  hasJarDiff,
  includesConfigs,
  includesFiles,
  recommendedBatch,
  scopeReady,
  toConfigChanges,
  type WizardActivation,
  type WizardBatch,
  type WizardConfigPick,
  type WizardContent,
  type WizardScope,
  type WizardStepId,
} from './wizard-state'

interface GuidedWizardProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  namespaceId: number
  // 打开时预选的交付内容类型（空态任务卡带类型进向导）
  initialContent: WizardContent
  // 成单（已提交审批）后回调：父级选中该单打开详情面板
  onCreated: (detail: ChangeOrderDetail) => void
}

// 扫描目录范围默认值（最常见交付载荷所在目录）
const DEFAULT_SCAN_DIR = 'plugins/'

export default function GuidedWizard({
  open,
  onOpenChange,
  namespaceId,
  initialContent,
  onCreated,
}: GuidedWizardProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [content, setContent] = useState<WizardContent>(initialContent)
  const [stepIndex, setStepIndex] = useState(0)
  const [orderId, setOrderId] = useState<number | null>(null)
  const [source, setSource] = useState('')
  // 差异扫描的目录范围（服务器根内相对目录），重扫 / 重算沿用同一范围
  const [scanDir, setScanDir] = useState(DEFAULT_SCAN_DIR)
  const [scan, setScan] = useState<ScanResult | null>(null)
  const [picks, setPicks] = useState<WizardConfigPick[]>([])
  const [scope, setScope] = useState<WizardScope>({ mode: 'all', regions: [], zones: [], servers: [] })
  const [batch, setBatch] = useState<WizardBatch>(() => recommendedBatch(null))
  const [activation, setActivation] = useState<WizardActivation>('push_only')
  const [title, setTitle] = useState('')
  const [prepared, setPrepared] = useState(0)
  const [errorText, setErrorText] = useState<string | null>(null)
  // 提交成功后关闭时不再删除草稿
  const keepOrderRef = useRef(false)

  // 每次打开重置全部状态并应用预选类型
  useEffect(() => {
    if (open) {
      setContent(initialContent)
      setStepIndex(0)
      setOrderId(null)
      setSource('')
      setScanDir(DEFAULT_SCAN_DIR)
      setScan(null)
      setPicks([])
      setScope({ mode: 'all', regions: [], zones: [], servers: [] })
      setBatch(recommendedBatch(null))
      setActivation('push_only')
      setTitle('')
      setPrepared(0)
      setErrorText(null)
      keepOrderRef.current = false
    }
  }, [open, initialContent])

  const steps = activeSteps(content)
  const current: WizardStepId = steps[Math.min(stepIndex, steps.length - 1)]

  // 结构树 → 按当前范围估算目标台数（批次编排换算与总和校验用；与范围步共用查询缓存）
  const treeQuery = useQuery({
    queryKey: ['change-orders', 'wizard-zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    enabled: open,
  })
  const zoneCounts = useMemo(() => flattenZoneCounts(treeQuery.data), [treeQuery.data])
  const targetEstimate = estimateTargetTotal(scope, zoneCounts)

  // 标题：用户已填用用户的，否则按内容类型给默认
  const resolvedTitle = (): string => {
    if (title.trim() !== '') {
      return title.trim()
    }
    if (content === 'configs') {
      return t('delivery.changes.wizard.review.defaultTitle.configs', { count: picks.length })
    }
    return t(`delivery.changes.wizard.review.defaultTitle.${content}`, { source })
  }

  const toMessage = (error: unknown): string =>
    error instanceof ApiClientError || error instanceof Error ? error.message : String(error)

  const invalidateList = () => queryClient.invalidateQueries({ queryKey: ['change-orders'] })

  // 丢弃已建 draft（取消 / 改交付内容时）：失败仅残留可见草稿，列表中可手动删除
  const discardDraft = (id: number): void => {
    void deleteChangeOrder(id, '向导放弃草稿')
      .catch(() => undefined)
      .then(() => invalidateList())
  }

  // 第 2 步「扫描差异」：懒建 draft（或回填模板源与扫描范围）后调 diff-scan
  const scanMutation = useMutation({
    mutationFn: async () => {
      let id = orderId
      if (id === null) {
        const created = await createChangeOrder({
          namespaceId,
          title: resolvedTitle(),
          sourceServerId: source,
          scanDir,
        })
        id = created.id
      } else {
        await updateChangeOrder(id, { sourceServerId: source, scanDir })
      }
      const result = await diffScanChangeOrder(id)
      return { id, result }
    },
    onSuccess: ({ id, result }) => {
      setOrderId(id)
      setScan({ items: result.items, snapshotAt: result.diffSnapshotAt })
    },
    onError: (error) => {
      setErrorText(toMessage(error))
    },
  })

  // 第 4 → 5 步：把草稿同步到位（建单 / 范围 / 批次 / 生效方式 / 挂配置）后进入预览
  const prepareMutation = useMutation({
    mutationFn: async () => {
      let id = orderId
      if (id === null) {
        const created = await createChangeOrder({ namespaceId, title: resolvedTitle() })
        id = created.id
      }
      await updateChangeOrder(id, {
        title: resolvedTitle(),
        sourceServerId: includesFiles(content) && source !== '' ? source : null,
        // 含文件载荷时随单落扫描范围；纯配置单为空串（与契约默认一致）
        scanDir: includesFiles(content) ? scanDir : '',
        selector: buildSelector(scope),
        ...buildBatch(batch),
        activationMethod: activation,
        configChanges: includesConfigs(content) ? toConfigChanges(picks) : [],
      })
      return id
    },
    onSuccess: (id) => {
      setOrderId(id)
      if (title.trim() === '') {
        setTitle(resolvedTitle())
      }
      setPrepared((n) => n + 1)
      setStepIndex((i) => i + 1)
    },
    onError: (error) => {
      setErrorText(toMessage(error))
    },
  })

  // 第 5 步「提交审批」：落最终标题后提审，成单交回父级打开详情
  const submitMutation = useMutation({
    mutationFn: async () => {
      if (orderId === null) {
        throw new Error(t('delivery.changes.wizard.review.notReady'))
      }
      await updateChangeOrder(orderId, { title: resolvedTitle() })
      return submitChangeOrder(orderId)
    },
    onSuccess: async (detail) => {
      keepOrderRef.current = true
      await invalidateList()
      onCreated(detail)
      onOpenChange(false)
    },
    onError: (error) => {
      setErrorText(toMessage(error))
    },
  })

  // 改交付内容：已建 draft 作废重来（避免残留过期的模板源 / 差异项）
  const handleContentChange = (next: WizardContent): void => {
    if (next === content) {
      return
    }
    if (orderId !== null) {
      discardDraft(orderId)
    }
    setContent(next)
    setOrderId(null)
    setScan(null)
    setPrepared(0)
    setErrorText(null)
  }

  // 关闭（取消 / ESC / 右上角 X）：未成单则清掉已建 draft
  const handleOpenChange = (next: boolean): void => {
    if (!next && !keepOrderRef.current && orderId !== null) {
      discardDraft(orderId)
    }
    onOpenChange(next)
  }

  // 当前步是否允许「下一步 / 提交」
  const canProceed = (): boolean => {
    switch (current) {
      case 'content':
        return true
      case 'source':
        return source !== '' && scan !== null
      case 'config':
        return picks.length > 0
      case 'scope':
        // 范围已选出目标 + 批次编排通过校验（百分比合计 100 / 台数合计等于目标数）
        return scopeReady(scope) && batchIssue(batch, targetEstimate) === null
      case 'review':
        return title.trim() !== ''
    }
  }

  const pending = scanMutation.isPending || prepareMutation.isPending || submitMutation.isPending

  const handleNext = (): void => {
    setErrorText(null)
    if (current === 'scope') {
      prepareMutation.mutate()
      return
    }
    if (current === 'review') {
      submitMutation.mutate()
      return
    }
    setStepIndex((i) => i + 1)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('delivery.changes.wizard.title')}</DialogTitle>
        </DialogHeader>

        <Stepper content={content} current={current} />

        {/* 步骤主体：往返切换保留各步状态。
            overflow-y-auto 会连带裁切横向溢出，故左右各留内边距容纳卡片阴影 / ring，
            再以等量负外边距抵消，保持与上方步骤条对齐。 */}
        <div className="-mx-1.5 max-h-[62vh] min-h-[280px] overflow-y-auto px-1.5">
          {current === 'content' && <WizardStepContent value={content} onChange={handleContentChange} />}
          {current === 'source' && (
            <WizardStepSource
              namespaceId={namespaceId}
              source={source}
              onSourceChange={(next) => {
                setSource(next)
                setScan(null)
              }}
              scanDir={scanDir}
              onScanDirChange={(next) => {
                // 改扫描范围即作废已扫差异（须按新范围重扫）
                setScanDir(next)
                setScan(null)
              }}
              scan={scan}
              scanning={scanMutation.isPending}
              onScan={() => {
                setErrorText(null)
                scanMutation.mutate()
              }}
              errorText={errorText}
              orderId={orderId}
            />
          )}
          {current === 'config' && (
            <WizardStepConfig
              namespaceId={namespaceId}
              picks={picks}
              onAddMany={(added) => {
                setPicks((prev) => [
                  ...prev.filter((p) => !added.some((a) => a.fileId === p.fileId)),
                  ...added,
                ])
              }}
              onRemoveMany={(fileIds) => {
                setPicks((prev) => prev.filter((p) => !fileIds.includes(p.fileId)))
              }}
            />
          )}
          {current === 'scope' && (
            <WizardStepScope
              namespaceId={namespaceId}
              scope={scope}
              onScopeChange={setScope}
              batch={batch}
              onBatchChange={setBatch}
              targetEstimate={targetEstimate}
              activation={activation}
              hasJar={hasJarDiff(scan?.items ?? [])}
              onActivationChange={setActivation}
            />
          )}
          {current === 'review' && (
            <WizardStepReview
              orderId={orderId}
              prepared={prepared}
              namespaceId={namespaceId}
              picks={picks}
              scope={scope}
              batch={batch}
              title={title}
              onTitleChange={setTitle}
            />
          )}
        </div>

        {errorText !== null && current !== 'source' && (
          <p className="text-sm text-destructive">{errorText}</p>
        )}

        {/* 底部导航：上一步 / 取消 / 下一步（末步为提交审批） */}
        <div className="flex items-center justify-between gap-2 border-t border-border pt-3">
          <Button
            variant="ghost"
            onClick={() => {
              handleOpenChange(false)
            }}
          >
            {t('delivery.changes.wizard.cancel')}
          </Button>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              disabled={stepIndex === 0 || pending}
              onClick={() => {
                setErrorText(null)
                setStepIndex((i) => Math.max(0, i - 1))
              }}
            >
              {t('delivery.changes.wizard.prev')}
            </Button>
            <Button disabled={!canProceed() || pending} onClick={handleNext}>
              {prepareMutation.isPending
                ? t('delivery.changes.wizard.preparing')
                : current === 'review'
                  ? t('delivery.changes.wizard.submit')
                  : t('delivery.changes.wizard.next')}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

// 顶部步骤条：画布 5 步固定展示；当前步高亮、已完成打勾、被跳过的步骤置灰标注
function Stepper({ content, current }: { content: WizardContent; current: WizardStepId }) {
  const { t } = useTranslation()
  const active = activeSteps(content)
  const currentActiveIndex = active.indexOf(current)

  return (
    <ol className="flex flex-wrap items-center gap-1.5">
      {WIZARD_STEPS.map((step, index) => {
        const activeIndex = active.indexOf(step)
        const skipped = activeIndex === -1
        const done = !skipped && activeIndex < currentActiveIndex
        const isCurrent = step === current
        return (
          <li
            key={step}
            aria-current={isCurrent ? 'step' : undefined}
            className={cn('flex items-center gap-1.5', index < WIZARD_STEPS.length - 1 && 'after:h-px after:w-4 after:bg-border after:content-[""]')}
          >
            <span
              className={cn(
                'grid size-5 shrink-0 place-items-center rounded-full text-[11px] font-semibold',
                isCurrent && 'bg-brand text-white',
                done && 'bg-brand-50 text-brand ring-1 ring-brand-200',
                !isCurrent && !done && !skipped && 'bg-surface-2 text-ink-3 ring-1 ring-border',
                skipped && 'bg-surface-2 text-ink-3/50 ring-1 ring-border',
              )}
            >
              {done ? <Check className="size-3" aria-hidden /> : index + 1}
            </span>
            <span
              className={cn(
                'text-xs',
                isCurrent ? 'font-semibold text-ink-1' : done ? 'text-ink-2' : 'text-ink-3',
                skipped && 'text-ink-3/50 line-through decoration-ink-3/40',
              )}
              title={skipped ? t('delivery.changes.wizard.stepSkipped') : undefined}
            >
              {t(`delivery.changes.wizard.steps.${step}`)}
            </span>
          </li>
        )
      })}
    </ol>
  )
}
