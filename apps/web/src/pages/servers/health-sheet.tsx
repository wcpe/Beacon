// 健康详情抽屉（Sheet）：因子分解 + 不可调度原因。抽屉形态不遮挡主流程。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CircleCheck, CircleX } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  cn,
  levelSolid,
  levelText,
} from '@beacon/ui'

import { fetchHealthDetail } from '../../api/cluster'
import { LEVEL_META, badgeOf } from './health-level'

interface HealthSheetProps {
  // 打开的 serverId；null 表示关闭
  serverId: string | null
  onOpenChange: (open: boolean) => void
}

export default function HealthSheet({ serverId, onOpenChange }: HealthSheetProps) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['health-detail', serverId],
    queryFn: () => fetchHealthDetail(serverId ?? ''),
    enabled: serverId !== null,
  })
  const detail = query.data
  const level = detail ? (LEVEL_META[detail.level] ?? 'warn') : 'warn'

  return (
    <Sheet open={serverId !== null} onOpenChange={onOpenChange}>
      {/* 加宽健康详情面板到 32rem（窄屏不超 90vw），让因子分解表从容展开。
          必须用与组件默认值同变体栈的 data-[side=right]:sm: 前缀，tailwind-merge 才会
          去重掉默认 data-[side=right]:sm:max-w-sm（裸 sm:max-w-* 会输在选择器特异性上）。 */}
      <SheetContent className="w-full gap-0 overflow-y-auto data-[side=right]:sm:max-w-[min(32rem,90vw)]">
        <SheetHeader>
          <SheetTitle>{t('cluster.servers.health.title')}</SheetTitle>
          <SheetDescription className="font-mono">{serverId}</SheetDescription>
        </SheetHeader>
        <div className="grid gap-4 px-4 pb-6">
          <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
            {detail && (
              <>
                {/* 健康分总览卡：大号分值 + 等级 / 可调度药丸 + 分值条 */}
                <div className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
                  <div className="flex items-end gap-3">
                    <span className={cn('text-[34px] leading-none font-bold tracking-[-1px] tnum', levelText(level))}>
                      {detail.score}
                    </span>
                    <div className="mb-1 flex flex-col gap-1.5">
                      <span className="text-[11px] text-ink-4">{t('cluster.servers.health.score')}</span>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge variant={badgeOf(level)} className="gap-1.5">
                          <span className="size-1.5 rounded-full bg-current" />
                          {t(`cluster.servers.health.level_${detail.level}`)}
                        </Badge>
                        <Badge variant={detail.schedulable ? 'ok' : 'crit'} className="gap-1">
                          {detail.schedulable ? (
                            <CircleCheck className="size-3" />
                          ) : (
                            <CircleX className="size-3" />
                          )}
                          {detail.schedulable
                            ? t('cluster.servers.health.schedulable')
                            : t('cluster.servers.health.notSchedulable')}
                        </Badge>
                      </div>
                    </div>
                  </div>
                  <span className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
                    <span
                      className={cn('block h-full rounded-full', levelSolid(level))}
                      style={{ width: `${String(Math.max(0, Math.min(100, detail.score)))}%` }}
                    />
                  </span>
                  <span className="text-[11px] text-ink-4">
                    {t('cluster.servers.health.weightsRev')} #{detail.weightsRev}
                  </span>
                </div>

                {detail.reasons.length > 0 && (
                  <div className="space-y-1.5">
                    <p className="text-[12.5px] font-semibold text-ink-1">{t('cluster.servers.health.reasons')}</p>
                    <ul className="flex flex-wrap gap-1.5">
                      {detail.reasons.map((reason) => (
                        <li key={reason}>
                          <Badge variant="crit" className="gap-1.5">
                            <span className="size-1.5 rounded-full bg-current" />
                            {t(`cluster.servers.schedReason.${reason}`, { defaultValue: reason })}
                          </Badge>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                <div className="space-y-1.5">
                  <p className="text-[12.5px] font-semibold text-ink-1">{t('cluster.servers.health.factors')}</p>
                  <Table className="tnum">
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t('cluster.servers.health.factor')}</TableHead>
                        <TableHead>{t('cluster.servers.health.normalized')}</TableHead>
                        <TableHead>{t('cluster.servers.health.weight')}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {detail.factors.map((factor) => (
                        <TableRow key={factor.factor} className={factor.applicable ? undefined : 'opacity-45'}>
                          <TableCell className="text-ink-2">{factor.factor}</TableCell>
                          <TableCell>
                            {factor.applicable ? (
                              <div className="flex items-center gap-2">
                                <span className="h-1.5 w-14 overflow-hidden rounded-full bg-muted">
                                  <span
                                    className="block h-full rounded-full bg-brand"
                                    style={{ width: `${String(Math.max(0, Math.min(100, factor.normalized)))}%` }}
                                  />
                                </span>
                                <span className="w-9 text-right text-ink-2">{factor.normalized}</span>
                              </div>
                            ) : (
                              <span className="text-ink-4">{t('cluster.servers.health.notApplicable')}</span>
                            )}
                          </TableCell>
                          <TableCell className="text-ink-3">{factor.weight}</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              </>
            )}
          </AsyncSection>
        </div>
      </SheetContent>
    </Sheet>
  )
}
