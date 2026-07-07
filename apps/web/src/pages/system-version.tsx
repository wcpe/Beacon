// 版本与更新页（/system/version）：当前版本 / 渠道 / 检查更新 / 触发更新（进度轮询）/ 取消 / 回滚（高风险 + 原因）/ 代理测试。
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  MarkdownLite,
  SectionHeader,
} from '@beacon/ui'

import {
  ApiClientError,
  cancelUpdate,
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

export default function SystemVersionPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [triggerOpen, setTriggerOpen] = useState(false)
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [proxyResult, setProxyResult] = useState<{ ok: boolean; message: string } | null>(null)

  const checkQuery = useQuery({
    queryKey: ['update-check'],
    queryFn: fetchUpdateCheck,
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
    <section className="grid gap-6">
      <SectionHeader size="lg" title={t('system.version.title')} />

      {/* 版本信息卡 */}
      <AsyncSection isLoading={checkQuery.isLoading} isError={checkQuery.isError} error={checkQuery.error}>
        {check && (
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle className="text-base">{t('system.version.title')}</CardTitle>
              <Button
                size="sm"
                variant="outline"
                disabled={checkQuery.isFetching}
                onClick={() => {
                  void checkQuery.refetch()
                }}
              >
                {checkQuery.isFetching ? t('system.version.checking') : t('system.version.check')}
              </Button>
            </CardHeader>
            <CardContent className="grid gap-3">
              <div className="flex flex-wrap items-center gap-3 text-sm">
                <span className="text-muted-foreground">{t('system.version.current')}</span>
                <span className="font-semibold">{check.currentVersion}</span>
                <Badge variant="outline">
                  {check.channel === 'prerelease'
                    ? t('system.version.channelPrerelease')
                    : t('system.version.channelStable')}
                </Badge>
                {check.isDevBuild ? (
                  <Badge variant="secondary">{t('system.version.devBuild')}</Badge>
                ) : check.hasUpdate ? (
                  <Badge variant="secondary">
                    {t('system.version.hasUpdate')}: {check.latestVersion}
                  </Badge>
                ) : (
                  <Badge variant="outline">{t('system.version.upToDate')}</Badge>
                )}
              </div>
              <p className="text-xs text-muted-foreground">
                {t('system.version.checkedAt')}: {formatIso(check.checkedAt)}
              </p>

              {check.hasUpdate && check.releaseNotes !== '' && (
                <div className="rounded-md bg-muted/50 p-3 text-sm">
                  <p className="mb-1 font-medium">
                    {t('system.version.releaseNotes')} · {check.latestVersion}
                  </p>
                  <MarkdownLite source={check.releaseNotes} />
                  <p className="mt-2 text-xs text-muted-foreground">
                    {t('system.version.publishedAt')}: {formatIso(check.publishedAt)}
                  </p>
                </div>
              )}

              {/* 更新 / 回滚操作区 */}
              <div className="flex flex-wrap gap-2">
                <Button
                  disabled={!check.hasUpdate || check.isDevBuild}
                  onClick={() => {
                    setActionError(null)
                    setTriggerOpen(true)
                  }}
                >
                  {t('system.version.trigger')}
                </Button>
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
            </CardContent>
          </Card>
        )}
      </AsyncSection>

      {/* 更新进度 */}
      {progress && (
        <ProgressCard
          progress={progress}
          cancelling={cancelMutation.isPending}
          onCancel={() => {
            cancelMutation.mutate()
          }}
        />
      )}

      {/* 代理测试 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t('system.version.proxy.title')}</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-2">
          <p className="text-sm text-muted-foreground">{t('system.version.proxy.hint')}</p>
          <div className="flex items-center gap-3">
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
              <span className={proxyResult.ok ? 'text-sm text-green-600' : 'text-sm text-destructive'}>
                {proxyResult.message}
              </span>
            )}
          </div>
        </CardContent>
      </Card>

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
