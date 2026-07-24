// 版本与更新页（/system/version）：当前版本 / 渠道 / 检查更新 / 触发更新（进度轮询）/ 取消 / 回滚（高风险 + 原因）/ 代理测试。
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { CheckCircle2, Cloud, DownloadCloud, PackageCheck, Rocket, Wifi } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  MarkdownLite,
  PageHeader,
  SectionHeader,
} from '@beacon/ui'

import {
  ApiClientError,
  cancelUpdate,
  fetchSettings,
  fetchUpdateCheck,
  fetchUpdateProgress,
  rollbackUpdate,
  testProxy,
  triggerUpdate,
} from '../api/system'
import SystemReasonDialog from '../features/system/reason-dialog'
import { formatIso } from '../features/system/format'
import ProgressCard from './system-version/progress-card'

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

function updateCheckIntervalMs(items: { key: string; value: string }[] | undefined): number | false {
  const enabled = items?.find((item) => item.key === 'update.auto-check-enabled')?.value === 'true'
  const hours = Number(items?.find((item) => item.key === 'update.check-interval-hours')?.value)
  return enabled && Number.isFinite(hours) && hours >= 1 ? hours * 60 * 60 * 1000 : false
}

export default function SystemVersionPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [triggerOpen, setTriggerOpen] = useState(false)
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [proxyResult, setProxyResult] = useState<{ ok: boolean; message: string } | null>(null)

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: fetchSettings,
  })
  const autoCheckEnabled =
    settingsQuery.isSuccess &&
    settingsQuery.data.items.find((item) => item.key === 'update.auto-check-enabled')?.value === 'true'
  const checkInterval = autoCheckEnabled ? updateCheckIntervalMs(settingsQuery.data.items) : false

  const checkQuery = useQuery({
    queryKey: ['update-check'],
    queryFn: () => fetchUpdateCheck(),
    enabled: autoCheckEnabled,
    // 登录后的低频检查由运维开关和小时周期控制；手动检查另走 force=true。
    refetchInterval: checkInterval,
  })

  const progressQuery = useQuery({
    queryKey: ['update-progress'],
    queryFn: fetchUpdateProgress,
    // 更新进行中时轮询进度
    refetchInterval: (query) => {
      const phase = query.state.data?.phase
      return phase !== undefined && phase !== 'idle' && phase !== 'failed' ? 2000 : false
    },
  })

  const invalidateProgress = () => queryClient.invalidateQueries({ queryKey: ['update-progress'] })

  const checkMutation = useMutation({
    mutationFn: () => fetchUpdateCheck(true),
    onSuccess: (result) => {
      queryClient.setQueryData(['update-check'], result)
    },
  })

  const triggerMutation = useMutation({
    mutationFn: triggerUpdate,
    onSuccess: async () => {
      await invalidateProgress()
      setTriggerOpen(false)
    },
    onError: (error) => {
      setActionError(messageOf(error))
    },
  })

  const cancelMutation = useMutation({
    mutationFn: cancelUpdate,
    onSuccess: async () => {
      await invalidateProgress()
    },
  })

  const rollbackMutation = useMutation({
    mutationFn: rollbackUpdate,
    onSuccess: async () => {
      await invalidateProgress()
      setRollbackOpen(false)
    },
    onError: (error) => {
      setActionError(messageOf(error))
    },
  })

  const proxyMutation = useMutation({
    mutationFn: testProxy,
    onSuccess: (result) => {
      setProxyResult({
        ok: result.ok,
        message: result.ok ? t('system.version.proxy.ok') : (result.error ?? t('system.version.proxy.fail')),
      })
    },
    onError: (error) => {
      setProxyResult({ ok: false, message: messageOf(error) })
    },
  })

  const check = checkQuery.data
  const progress = progressQuery.data
  const rollbackAvailable = progress?.rollbackAvailable ?? false

  return (
    <section className="grid gap-5">
      <PageHeader icon={<PackageCheck className="size-5" />} title={t('nav.systemVersion')} />

      {/* 版本信息卡（紧凑：版本号 + 渠道 / 状态 + 检查 + 更新说明，不含操作按钮） */}
      <AsyncSection isLoading={checkQuery.isLoading} isError={checkQuery.isError} error={checkQuery.error}>
        <section className="grid gap-3">
          <SectionHeader icon={<Rocket className="size-4" />} title={t('system.version.sections.info')} />
          <div className="grid gap-4 rounded-xl border border-border bg-card p-5 shadow-card">
            {/* 当前版本主行：图标框 + 大版本号 + 渠道 / 状态药丸 + 检查更新 */}
            <div className="flex flex-wrap items-center gap-3">
              {check && (
                <>
                  <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-brand-50 text-brand" aria-hidden>
                    <Rocket className="size-5" />
                  </span>
                  <div className="min-w-0">
                    <div className="text-[11.5px] font-medium text-ink-3">{t('system.version.current')}</div>
                    <div className="text-[22px] leading-none font-bold tracking-[-0.5px] text-ink-1 tnum">
                      {check.currentVersion}
                    </div>
                  </div>
                  <Badge variant="off" className="ml-1">
                    {t('system.version.channelStable')}
                  </Badge>
                  {check.status === 'check-failed' ? (
                    <Badge variant="warn" className="gap-1.5">
                      <span className="size-1.5 rounded-full bg-current" />
                      {t('system.version.checkFail')}
                    </Badge>
                  ) : check.isDevBuild ? (
                    <Badge variant="brand">{t('system.version.devBuild')}</Badge>
                  ) : check.hasUpdate ? (
                    <Badge variant="warn" className="gap-1.5">
                      <span className="size-1.5 rounded-full bg-current" />
                      {t('system.version.hasUpdate')}: {check.latestVersion}
                    </Badge>
                  ) : (
                    <Badge variant="ok" className="gap-1.5">
                      <CheckCircle2 className="size-3" />
                      {t('system.version.upToDate')}
                    </Badge>
                  )}
                </>
              )}
              <Button
                size="sm"
                variant="outline"
                className="ml-auto"
                disabled={checkQuery.isFetching || checkMutation.isPending}
                onClick={() => {
                  checkMutation.mutate()
                }}
              >
                {checkQuery.isFetching || checkMutation.isPending ? t('system.version.checking') : t('system.version.check')}
              </Button>
            </div>

            {check && (
              <>
                <p className="text-xs text-ink-4">
                  {t('system.version.checkedAt')}: {formatIso(check.checkedAt)}
                </p>

                {check.status === 'check-failed' && check.failureReason && (
                  <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
                    {check.failureReason}
                  </p>
                )}

                {check.status === 'ok' && check.hasUpdate && check.releaseNotes !== '' && (
                  <div className="rounded-lg border border-border bg-surface-2 p-4 text-sm">
                    <p className="mb-1.5 flex items-center gap-1.5 font-semibold text-ink-1">
                      <Cloud className="size-4 text-brand" />
                      {t('system.version.releaseNotes')} · {check.latestVersion}
                    </p>
                    <MarkdownLite source={check.releaseNotes} />
                    <p className="mt-2 text-xs text-ink-4">
                      {t('system.version.publishedAt')}: {formatIso(check.publishedAt)}
                    </p>
                  </div>
                )}
              </>
            )}
          </div>
        </section>
      </AsyncSection>

      {/* 更新与渠道 / 维护操作：紧凑双栏，避免长页堆叠 */}
      <div className="grid gap-5 lg:grid-cols-2">
        {/* 更新与渠道：触发更新 + 更新进度（常驻可见，不被顶下去） */}
        <section className="grid gap-3">
          <SectionHeader icon={<DownloadCloud className="size-4" />} title={t('system.version.sections.update')} />
          <div className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
            <Button
              disabled={!check || check.status === 'check-failed' || !check.hasUpdate || check.isDevBuild}
              onClick={() => {
                setActionError(null)
                setTriggerOpen(true)
              }}
            >
              {t('system.version.trigger')}
            </Button>
            {progress && progress.phase !== 'idle' ? (
              <ProgressCard
                progress={progress}
                cancelling={cancelMutation.isPending}
                onCancel={() => {
                  cancelMutation.mutate()
                }}
              />
            ) : (
              <p className="text-sm text-ink-4">{t('system.version.progress.idleHint')}</p>
            )}
          </div>
        </section>

        {/* 维护操作：回滚 + 出站代理测试 */}
        <section className="grid gap-3">
          <SectionHeader icon={<Wifi className="size-4" />} title={t('system.version.sections.maintenance')} />
          <div className="grid gap-4 rounded-xl border border-border bg-card p-4 shadow-card">
            <div className="grid gap-2">
              <Button
                variant="outline"
                disabled={!rollbackAvailable}
                onClick={() => {
                  setActionError(null)
                  setRollbackOpen(true)
                }}
              >
                {rollbackAvailable ? t('system.version.rollback') : t('system.version.rollbackUnavailable')}
              </Button>
            </div>
            <div className="grid gap-2.5 border-t border-border pt-3">
              <p className="text-sm text-ink-3">{t('system.version.proxy.hint')}</p>
              <div className="flex flex-wrap items-center gap-3">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={proxyMutation.isPending}
                  onClick={() => {
                    setProxyResult(null)
                    proxyMutation.mutate()
                  }}
                >
                  {proxyMutation.isPending ? t('system.version.proxy.testing') : t('system.version.proxy.test')}
                </Button>
                {proxyResult !== null && (
                  <Badge variant={proxyResult.ok ? 'ok' : 'crit'} className="gap-1.5">
                    <span className="size-1.5 rounded-full bg-current" />
                    {proxyResult.message}
                  </Badge>
                )}
              </div>
            </div>
          </div>
        </section>
      </div>

      {/* 触发更新确认（无需原因，但二次确认） */}
      <SystemReasonDialog
        open={triggerOpen}
        onOpenChange={setTriggerOpen}
        title={t('system.version.confirmTriggerTitle', { version: check?.latestVersion ?? '' })}
        description={t('system.version.confirmTriggerDesc')}
        confirmLabel={t('system.version.confirmTrigger')}
        cancelLabel={t('system.common.cancel')}
        reasonLabel=""
        requireReason={false}
        pending={triggerMutation.isPending}
        errorText={actionError}
        onConfirm={() => {
          triggerMutation.mutate()
        }}
      />

      {/* 回滚确认（高风险 + 原因必填） */}
      <SystemReasonDialog
        open={rollbackOpen}
        onOpenChange={setRollbackOpen}
        title={t('system.version.confirmRollbackTitle')}
        description={t('system.version.confirmRollbackDesc')}
        confirmLabel={t('system.version.confirmRollback')}
        cancelLabel={t('system.common.cancel')}
        reasonLabel={t('system.version.rollbackReasonLabel')}
        reasonPlaceholder={t('system.version.rollbackReasonPlaceholder')}
        pending={rollbackMutation.isPending}
        errorText={actionError}
        onConfirm={() => {
          rollbackMutation.mutate()
        }}
      />
    </section>
  )
}
