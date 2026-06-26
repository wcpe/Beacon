// 告警事件信息流页（FR-89，见 ADR-0041）：系统健康事件历史留痕的时间线，
// 按类型 / 级别 / 环境 / 时间范围过滤、分页（时间倒序）。区别于审计日志（人的操作）。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { listAlertEvents } from '../api/client'
import type { AlertEventFilter } from '../api/client'
import type { AlertEventView } from '../api/types'
import { formatTime } from '../api/format'
import AsyncSection from '@/components/AsyncSection'
import { usePageHeader } from '@/components/PageHeader'
import { useEnvironment } from '@/state/environment'
import SummaryStrip, { type SummaryItem } from '@/components/SummaryStrip'
import { Skeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

// 单页条数（固定，运维场景无需可配）
const PAGE_SIZE = 20
// 过滤下拉「全部」哨兵值（Select 不支持空串选项，映射回 undefined）
const ALL = 'all'
// 事件类型与级别枚举（与后端 model.AlertEvent 常量一致）
const TYPE_OPTIONS = ['health-transition', 'publish-fail', 'backend-unreachable']
const LEVEL_OPTIONS = ['info', 'warning', 'critical']

// 把表单里的本地时间值转成后端可识别的 ISO（UTC）字符串；空值返回 undefined
function toIso(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

// 级别 → Badge 变体：critical 红、warning 次级、其余轮廓
function levelVariant(level: string): 'destructive' | 'secondary' | 'outline' {
  if (level === 'critical') return 'destructive'
  if (level === 'warning') return 'secondary'
  return 'outline'
}

// 级别 → 时间线圆点颜色
function levelDotClass(level: string): string {
  if (level === 'critical') return 'bg-red-600'
  if (level === 'warning') return 'bg-amber-500'
  return 'bg-muted-foreground'
}

export default function AlertEventsPage() {
  const { t } = useTranslation()
  // 枚举英文 → 中文（未知回退原文）
  const typeLabel = (type: string) => t(`alertEvent.type.${type}`, { defaultValue: type })
  const levelLabel = (level: string) => t(`alertEvent.level.${level}`, { defaultValue: level })

  // 环境收口（FR-105 真机打磨）：环境改读页眉全局环境，不再页内自管 namespace 筛选；其它筛选维度（类型/级别/时间窗）保留页内。
  const namespace = useEnvironment()
  // 过滤表单草稿（点「查询」才生效）
  const [type, setType] = useState(ALL)
  const [level, setLevel] = useState(ALL)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  // 已生效查询条件（不含 namespace；namespace 由全局环境合并）
  const [filter, setFilter] = useState<AlertEventFilter>({ page: 1, size: PAGE_SIZE })
  // 详情 Dialog 选中条目（null 表示关闭）
  const [selected, setSelected] = useState<AlertEventView | null>(null)

  // 页眉（FR-105）：标题 + 副标题，环境范围页
  usePageHeader({
    title: t('alertEvent.title'),
    subtitle: t('alertEvent.subtitle'),
    envScoped: true,
  })

  // 生效过滤 = 页内筛选 + 全局环境（空串＝全部环境）。全局环境变化即重算 → queryKey 含其 namespace → 自动重查。
  const effectiveFilter = useMemo<AlertEventFilter>(
    () => ({ ...filter, namespace: namespace || undefined }),
    [filter, namespace],
  )

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['alert-events', effectiveFilter],
    queryFn: () => listAlertEvents(effectiveFilter),
    placeholderData: keepPreviousData,
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    // namespace 不在页内筛选；由全局环境合并进 effectiveFilter。
    setFilter({
      type: type === ALL ? undefined : type,
      level: level === ALL ? undefined : level,
      from: toIso(from),
      to: toIso(to),
      page: 1,
      size: PAGE_SIZE,
    })
  }

  function goPage(page: number) {
    setFilter((f) => ({ ...f, page }))
  }

  const total = data?.total ?? 0
  const page = filter.page ?? 1
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  // 顶部汇总条（FR-106）：本页条数 + 各级别计数（从已拉列表派生；无「未处理」字段故按级别给）。
  const items = data?.items ?? []
  const summaryItems: SummaryItem[] = [
    { label: t('alertEvent.summaryPage'), value: items.length },
    {
      label: t('alertEvent.summaryCritical'),
      value: items.filter((e) => e.level === 'critical').length,
      tone: 'danger',
    },
    {
      label: t('alertEvent.summaryWarning'),
      value: items.filter((e) => e.level === 'warning').length,
      tone: 'warning',
    },
    {
      label: t('alertEvent.summaryInfo'),
      value: items.filter((e) => e.level === 'info').length,
      tone: 'muted',
    },
  ]

  return (
    <div className="space-y-4">
      {/* 顶部汇总条（FR-106）：本页 + 各级别计数 */}
      <SummaryStrip items={summaryItems} />

      {/* 内联吸顶工具栏（FR-106）：原筛选 Card 压成一行紧凑控件，保留全部筛选维度与「查询」 */}
      <form
        onSubmit={onSearch}
        className="sticky top-0 z-10 flex flex-wrap items-center gap-2 bg-background py-1"
      >
        <Select value={type} onValueChange={setType}>
          <SelectTrigger id="ae-type" className="w-36" aria-label={t('alertEvent.typeLabel')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('alertEvent.allTypes')}</SelectItem>
            {TYPE_OPTIONS.map((v) => (
              <SelectItem key={v} value={v}>
                {typeLabel(v)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={level} onValueChange={setLevel}>
          <SelectTrigger id="ae-level" className="w-32" aria-label={t('alertEvent.levelLabel')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('alertEvent.allLevels')}</SelectItem>
            {LEVEL_OPTIONS.map((v) => (
              <SelectItem key={v} value={v}>
                {levelLabel(v)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {/* 环境收口（FR-105 真机打磨）：原页内环境筛选已移除，环境改读页眉全局环境槽。 */}
        <Input
          id="ae-from"
          aria-label={t('alertEvent.fromTime')}
          className="w-44"
          type="datetime-local"
          value={from}
          onChange={(e) => setFrom(e.target.value)}
        />
        <Input
          id="ae-to"
          aria-label={t('alertEvent.toTime')}
          className="w-44"
          type="datetime-local"
          value={to}
          onChange={(e) => setTo(e.target.value)}
        />
        <Button type="submit">{t('common.query')}</Button>
      </form>

      {/* 裸信息流（FR-106）：去 Card 外壳，保留时间线渲染（信息流语义更贴） */}
      <AsyncSection
        isLoading={isLoading}
        isError={isError}
        error={error}
        skeleton={
          // 时间线骨架：若干条事件行占位，贴近真实信息流形状
          <ol className="relative space-y-4 border-l pl-6">
            {Array.from({ length: 6 }).map((_, i) => (
              <li key={i} className="space-y-2">
                <div className="flex items-center gap-2">
                  <Skeleton className="h-5 w-14 rounded-md" />
                  <Skeleton className="h-4 w-48" />
                  <Skeleton className="h-5 w-20 rounded-md" />
                </div>
                <Skeleton className="h-3 w-40" />
              </li>
            ))}
          </ol>
        }
      >
        {data && data.items.length === 0 ? (
          <p className="py-8 text-center text-sm text-muted-foreground">{t('alertEvent.empty')}</p>
        ) : (
          <ol className="relative space-y-4 border-l pl-6">
            {data?.items.map((ev) => (
              <li key={ev.id} className="relative">
                {/* 时间线圆点（按级别着色） */}
                <span
                  aria-hidden
                  className={`absolute -left-[1.6rem] top-1.5 inline-block h-2.5 w-2.5 rounded-full ${levelDotClass(ev.level)}`}
                />
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant={levelVariant(ev.level)}>{levelLabel(ev.level)}</Badge>
                  <span className="text-sm font-medium">{ev.message}</span>
                  <Badge variant="outline">{typeLabel(ev.type)}</Badge>
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                  <span>{formatTime(ev.createdAt)}</span>
                  {ev.namespace && <span>{t('alertEvent.colNamespace')}: {ev.namespace}</span>}
                  {ev.serverId && <span className="font-mono">{ev.serverId}</span>}
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-auto px-1.5 py-0"
                    onClick={() => setSelected(ev)}
                  >
                    {t('common.view')}
                  </Button>
                </div>
              </li>
            ))}
          </ol>
        )}

        <div className="mt-4 flex items-center justify-center gap-4 text-sm">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={page <= 1 || isFetching}
            onClick={() => goPage(page - 1)}
          >
            {t('common.prevPage')}
          </Button>
          <span className="text-muted-foreground">
            {t('common.pageInfo', { page, total: totalPages, count: total })}
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={page >= totalPages || isFetching}
            onClick={() => goPage(page + 1)}
          >
            {t('common.nextPage')}
          </Button>
        </div>
      </AsyncSection>

      {/* 事件详情 Dialog：完整字段 + detail 原文 */}
      <Dialog open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('alertEvent.detailTitle')}</DialogTitle>
          </DialogHeader>
          {selected && (
            <div className="space-y-4 text-sm">
              <dl className="grid grid-cols-2 gap-x-4 gap-y-2">
                <div>
                  <dt className="text-muted-foreground">{t('alertEvent.colTime')}</dt>
                  <dd>{formatTime(selected.createdAt)}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('alertEvent.colType')}</dt>
                  <dd>{typeLabel(selected.type)}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('alertEvent.colLevel')}</dt>
                  <dd>
                    <Badge variant={levelVariant(selected.level)}>{levelLabel(selected.level)}</Badge>
                  </dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('alertEvent.colNamespace')}</dt>
                  <dd>{selected.namespace || '-'}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('alertEvent.colServerId')}</dt>
                  <dd className="font-mono break-all">{selected.serverId || '-'}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">{t('alertEvent.colMessage')}</dt>
                  <dd>{selected.message}</dd>
                </div>
              </dl>
              <div className="space-y-1.5">
                <div className="text-muted-foreground">{t('alertEvent.detailField')}</div>
                <pre className="max-h-80 overflow-auto rounded-md border bg-muted p-3 font-mono text-xs whitespace-pre-wrap break-all">
                  {selected.detail || '-'}
                </pre>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
