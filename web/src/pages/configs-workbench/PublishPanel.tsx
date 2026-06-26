// 发布 + 影响面板（改进 1，仿 FR-2 热推语义）：
// 受管层文件改一处影响多台服 —— 发布不是逐台拖，而是「按覆盖层发布 → 热推到所有受影响在线服」。
// 受管侧选中后点「发布选中 N 项」弹本面板：
//   ① 将发布：文件清单 + 覆盖层徽标 + 版本 vN→vN+1
//   ② 影响面：按覆盖层分组列出受影响服（在线点 + 有/无变化），附离线服「上线时各自拉取」说明
//   ③ 拓印审核门：有差异 N 台 → 批量审阅闸（勾「我已审阅全部 diff」才放行）+「查看」点开批量 diff
//   ④ 底部：发布后秒级热更提示 + 取消 +「发布并热推（N 台）」主按钮
// 纯前端 mock，用相对工作台容器的 absolute 覆盖层（非 fixed）。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FileText, GitCompare, Radio, Rocket, X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import CodeEditor from '@/components/CodeEditor'
import { cn } from '@/lib/utils'
import { imprintDiffs, type PublishImpactServer } from '@/api/mock/workbench'
import { SCOPE_META } from './diffMeta'
import { usePublishImpact } from './useWorkbenchData'

export default function PublishPanel({
  // 待发布的受管文件名（含路径，如 spawn.yml / Essentials/config.yml）
  names,
  // 确认发布并热推（传在线热推台数，供 toast）
  onPublish,
  onCancel,
}: {
  names: string[]
  onPublish: (onlineCount: number) => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const impact = usePublishImpact(names, true)
  // 批量审阅闸：有差异时须勾选才放行发布
  const [reviewed, setReviewed] = useState(false)
  // 批量 diff 子浮层开关
  const [diffOpen, setDiffOpen] = useState(false)

  const data = impact.data
  const driftCount = data?.driftCount ?? 0
  // 本次热推的在线台数（去重，跨组同一服只算一次）
  const onlineCount = useMemo(() => {
    if (!data) return 0
    const ids = new Set<string>()
    for (const g of data.groups) for (const s of g.servers) if (s.online) ids.add(s.serverId)
    return ids.size
  }, [data])
  // 有差异才需审阅门；无差异直接可发布
  const gatePassed = driftCount === 0 || reviewed

  return (
    <div className="absolute inset-0 z-50 flex items-center justify-center p-6">
      {/* 半透明遮罩 */}
      <div className="absolute inset-0 bg-background/70 backdrop-blur-sm" onClick={onCancel} aria-hidden />
      {/* 浮层卡 */}
      <div className="relative flex max-h-full w-full max-w-3xl flex-col overflow-hidden rounded-lg border border-border bg-card shadow-2xl">
        {/* 头 */}
        <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-4 py-3">
          <Rocket className="h-4 w-4 text-primary" />
          <div className="min-w-0">
            <div className="text-sm font-medium text-foreground">{t('configs.workbench.publishTitle')}</div>
            <div className="text-[0.65rem] text-muted-foreground">
              {t('configs.workbench.publishSubtitle', { count: names.length })}
            </div>
          </div>
          <button
            type="button"
            onClick={onCancel}
            aria-label={t('common.cancel')}
            className="ml-auto flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>

        {/* 主体（内部滚） */}
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto scrollbar-hide px-4 py-3">
          {impact.isLoading || !data ? (
            <div className="space-y-2">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-5 w-full" />
              ))}
            </div>
          ) : (
            <>
              {/* ① 将发布：文件清单 + 覆盖层 + 版本 vN→vN+1 */}
              <section>
                <h3 className="mb-1.5 text-[0.7rem] font-semibold text-muted-foreground">
                  {t('configs.workbench.publishWillTitle')}
                </h3>
                <div className="overflow-hidden rounded-md border border-border">
                  {data.files.map((f) => {
                    const meta = SCOPE_META[f.scope]
                    return (
                      <div
                        key={f.name}
                        className="flex items-center gap-2 border-b border-border/50 px-3 py-1.5 text-xs last:border-b-0"
                      >
                        <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                        <span className="min-w-0 flex-1 truncate font-mono text-foreground">{f.name}</span>
                        <Badge variant="outline" className={cn('h-4 shrink-0 px-1 text-[0.6rem]', meta.badgeClass)}>
                          {t(meta.labelKey)}
                        </Badge>
                        <span className="shrink-0 font-mono text-[0.65rem] text-muted-foreground">
                          v{f.fromVersion} → <span className="text-foreground">v{f.toVersion}</span>
                        </span>
                      </div>
                    )
                  })}
                </div>
              </section>

              {/* ② 影响面：按覆盖层分组的受影响服 */}
              <section>
                <h3 className="mb-1.5 flex items-center gap-1.5 text-[0.7rem] font-semibold text-muted-foreground">
                  <Radio className="h-3.5 w-3.5 text-emerald-500" />
                  {t('configs.workbench.publishImpactTitle', { count: onlineCount })}
                </h3>
                <div className="space-y-2">
                  {data.groups.map((g) => {
                    const meta = SCOPE_META[g.scope]
                    return (
                      <div key={g.label} className="rounded-md border border-border px-3 py-2">
                        <div className="mb-1.5 flex items-center gap-1.5 text-[0.7rem]">
                          <Badge variant="outline" className={cn('h-4 px-1 text-[0.6rem]', meta.badgeClass)}>
                            {t(meta.labelKey)}
                          </Badge>
                          <span className="font-medium text-foreground">{g.label}</span>
                          <span className="text-muted-foreground">
                            {t('configs.workbench.publishGroupCount', { count: g.servers.length })}
                          </span>
                        </div>
                        <div className="flex flex-wrap gap-1.5">
                          {g.servers.map((s) => (
                            <ServerChip key={s.serverId} server={s} />
                          ))}
                        </div>
                      </div>
                    )
                  })}
                </div>
                <p className="mt-1.5 text-[0.65rem] text-muted-foreground/80">
                  {t('configs.workbench.publishOfflineNote')}
                </p>
              </section>

              {/* ③ 拓印审核门：有差异时一行提示 + 批量审阅闸 + 查看批量 diff */}
              {driftCount > 0 && (
                <section className="rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2">
                  <div className="flex items-center gap-2 text-xs">
                    <GitCompare className="h-3.5 w-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
                    <span className="font-medium text-foreground">
                      {t('configs.workbench.publishImprintGate', { count: driftCount })}
                    </span>
                    <Button
                      variant="outline"
                      size="xs"
                      className="ml-auto h-6 text-[0.65rem]"
                      onClick={() => setDiffOpen(true)}
                    >
                      {t('configs.workbench.publishImprintView')}
                    </Button>
                  </div>
                  <label className="mt-2 flex cursor-pointer select-none items-center gap-1.5 text-xs text-muted-foreground">
                    <input
                      type="checkbox"
                      checked={reviewed}
                      onChange={(e) => setReviewed(e.target.checked)}
                      className="h-3 w-3 accent-primary"
                    />
                    {t('configs.workbench.publishImprintReviewed')}
                  </label>
                </section>
              )}
            </>
          )}
        </div>

        {/* 底部：热更提示 + 取消 + 发布并热推 */}
        <div className="flex shrink-0 items-center gap-3 border-t border-border px-4 py-3">
          <span className="text-[0.65rem] text-muted-foreground">{t('configs.workbench.publishHotReloadNote')}</span>
          <div className="ml-auto flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={onCancel}>
              {t('common.cancel')}
            </Button>
            <Button size="sm" disabled={!gatePassed || impact.isLoading} onClick={() => onPublish(onlineCount)}>
              <Rocket className="mr-1 h-3.5 w-3.5" />
              {t('configs.workbench.publishConfirm', { count: onlineCount })}
            </Button>
          </div>
        </div>
      </div>

      {/* 批量 diff 子浮层（查看各文件 期望值 ⟷ 服务器现状） */}
      {diffOpen && data && (
        <BatchDiffOverlay names={data.files.map((f) => f.name)} onClose={() => setDiffOpen(false)} />
      )}
    </div>
  )
}

// 受影响服务器 chip：在线点 + serverId + 有/无变化
function ServerChip({ server }: { server: PublishImpactServer }) {
  const { t } = useTranslation()
  return (
    <span
      className={cn(
        'flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-[0.65rem]',
        server.online ? 'border-border bg-background' : 'border-border/50 bg-muted/30 opacity-70',
      )}
    >
      <span
        className={cn('h-1.5 w-1.5 rounded-full', server.online ? 'bg-emerald-500' : 'bg-muted-foreground/40')}
      />
      <span className="font-mono text-foreground">{server.serverId}</span>
      {server.online ? (
        server.changed ? (
          <span className="text-amber-600 dark:text-amber-400">{t('configs.workbench.publishChanged')}</span>
        ) : (
          <span className="text-muted-foreground">{t('configs.workbench.publishUnchanged')}</span>
        )
      ) : (
        <span className="text-muted-foreground">{t('configs.workbench.publishOffline')}</span>
      )}
    </span>
  )
}

// 批量 diff 子浮层（复用 ImprintReviewOverlay 思路，多文件竖排切换）：
// 左侧文件名列，右侧 Monaco DiffEditor 展示选中文件的 期望值 ⟷ 服务器现状。
function BatchDiffOverlay({ names, onClose }: { names: string[]; onClose: () => void }) {
  const { t } = useTranslation()
  // 仅取有 mock diff 的文件（其余无差异不进批量审阅）
  const diffNames = useMemo(() => names.filter((n) => imprintDiffs[n.split('/').pop() ?? n]), [names])
  const [active, setActive] = useState(diffNames[0] ?? names[0] ?? '')
  const activeBase = active.split('/').pop() ?? active
  const diff = imprintDiffs[activeBase] ?? { expected: '', current: '' }

  return (
    <div className="absolute inset-0 z-[60] flex items-center justify-center p-6">
      <div className="absolute inset-0 bg-background/70 backdrop-blur-sm" onClick={onClose} aria-hidden />
      <div className="relative flex max-h-full w-full max-w-4xl flex-col overflow-hidden rounded-lg border border-border bg-card shadow-2xl">
        {/* 头 */}
        <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-4 py-3">
          <GitCompare className="h-4 w-4 text-muted-foreground" />
          <span className="text-sm font-medium text-foreground">{t('configs.workbench.publishBatchDiffTitle')}</span>
          <Badge variant="secondary" className="ml-1 h-4 px-1.5 text-[0.6rem]">
            {t('configs.workbench.publishBatchDiffCount', { count: diffNames.length })}
          </Badge>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('common.cancel')}
            className="ml-auto flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
        {/* 主体：左文件列 + 右 diff */}
        <div className="flex min-h-0 flex-1">
          <div className="w-48 shrink-0 overflow-y-auto scrollbar-hide border-r border-border py-1">
            {diffNames.map((n) => (
              <button
                key={n}
                type="button"
                onClick={() => setActive(n)}
                className={cn(
                  'flex w-full items-center gap-1.5 px-3 py-1.5 text-left text-xs transition-colors',
                  n === active ? 'bg-primary/10 font-medium text-foreground' : 'text-muted-foreground hover:bg-muted/50',
                )}
              >
                <FileText className="h-3.5 w-3.5 shrink-0" />
                <span className="truncate font-mono">{n}</span>
              </button>
            ))}
          </div>
          <div className="min-w-0 flex-1">
            {/* diff 列头 */}
            <div className="flex shrink-0 items-center border-b border-border bg-muted/20 text-[0.65rem] font-medium text-muted-foreground">
              <span className="flex-1 px-4 py-1.5">{t('configs.workbench.imprintExpected')}</span>
              <span className="flex-1 px-4 py-1.5">{t('configs.workbench.imprintCurrent')}</span>
            </div>
            <div className="h-80">
              <CodeEditor original={diff.current} modified={diff.expected} language="yaml" />
            </div>
          </div>
        </div>
        {/* 底部：关闭 */}
        <div className="flex shrink-0 items-center justify-end border-t border-border px-4 py-3">
          <Button variant="outline" size="sm" onClick={onClose}>
            {t('common.cancel')}
          </Button>
        </div>
      </div>
    </div>
  )
}
