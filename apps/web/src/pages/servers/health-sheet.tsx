// 健康详情抽屉（Sheet）：因子分解 + 不可调度原因。抽屉形态不遮挡主流程。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

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
} from '@beacon/ui'

import { fetchHealthDetail } from '../../api/cluster'

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

  return (
    <Sheet open={serverId !== null} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{t('cluster.servers.health.title')}</SheetTitle>
          <SheetDescription>{serverId}</SheetDescription>
        </SheetHeader>
        <div className="grid gap-4 px-4 pb-6">
          <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
            {detail && (
              <>
                <div className="flex flex-wrap items-center gap-3 text-sm">
                  <span className="text-muted-foreground">{t('cluster.servers.health.score')}</span>
                  <span className="text-lg font-semibold">{detail.score}</span>
                  <Badge variant="outline">{t(`cluster.servers.health.level_${detail.level}`)}</Badge>
                  <Badge variant={detail.schedulable ? 'secondary' : 'destructive'}>
                    {detail.schedulable
                      ? t('cluster.servers.health.schedulable')
                      : t('cluster.servers.health.notSchedulable')}
                  </Badge>
                  <span className="text-xs text-muted-foreground">
                    {t('cluster.servers.health.weightsRev')} #{detail.weightsRev}
                  </span>
                </div>

                {detail.reasons.length > 0 && (
                  <div className="space-y-1">
                    <p className="text-sm font-medium">{t('cluster.servers.health.reasons')}</p>
                    <ul className="flex flex-wrap gap-1.5">
                      {detail.reasons.map((reason) => (
                        <li key={reason}>
                          <Badge variant="destructive">{reason}</Badge>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                <div className="space-y-1">
                  <p className="text-sm font-medium">{t('cluster.servers.health.factors')}</p>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{t('cluster.servers.health.factor')}</TableHead>
                        <TableHead>{t('cluster.servers.health.normalized')}</TableHead>
                        <TableHead>{t('cluster.servers.health.weight')}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {detail.factors.map((factor) => (
                        <TableRow key={factor.factor} className={factor.applicable ? undefined : 'opacity-50'}>
                          <TableCell>{factor.factor}</TableCell>
                          <TableCell>
                            {factor.applicable
                              ? factor.normalized
                              : t('cluster.servers.health.notApplicable')}
                          </TableCell>
                          <TableCell>{factor.weight}</TableCell>
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
