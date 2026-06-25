// 控制面在线更新模态框（FR-100，消费 FR-99 端点）。
// 展示当前 / 可用版本、渠道、release 日志（安全渲染为纯文本）、发布时间、release 外链；
// 「立即检查」绕服务端缓存强制刷新；「立即更新」二次确认（复用 FR-76 范式）后触发应用更新、
// 轮询进度（phase/percent），重启发生后复用 FR-78 连接指示自动重连、回显新版本。
//
// 安全渲染：releaseNotes 直接作为文本子节点交由 React 转义（whitespace-pre-wrap 保留换行），
// 绝不用 dangerouslySetInnerHTML 注入 release 正文原文（防 XSS）。

import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ExternalLink, RefreshCw, Download } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import DestructiveConfirmDialog from '@/components/DestructiveConfirmDialog'
import { useMessage } from '@/components/useMessage'
import { useConnectionStatus } from '@/hooks/useConnectionStatus'
import { ApiClientError, triggerUpdate, updateProgress } from '@/api/client'
import { formatTime } from '@/api/format'
import type { UpdateCheckView, UpdateProgressView } from '@/api/types'

// 进度轮询周期（毫秒）：触发应用后短周期反映 phase/percent，直到重启断连。
const PROGRESS_POLL_MS = 1500

interface UpdateModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  // 检查结果（由 SystemHeader 经 useUpdateCheck 提供，避免重复订阅）
  data: UpdateCheckView | undefined
  isLoading: boolean
  isError: boolean
  // 「立即检查」：强制刷新（?force=true），返回 Promise 供按钮态联动
  onRefresh: () => Promise<unknown>
}

// 把进度阶段映射为中文进度文案。
function phaseText(t: (k: string, opts?: Record<string, unknown>) => string, p: UpdateProgressView): string {
  switch (p.phase) {
    case 'checking':
      return t('updateModal.phaseChecking')
    case 'downloading':
      return t('updateModal.phaseDownloading', { percent: p.percent })
    case 'verifying':
      return t('updateModal.phaseVerifying')
    case 'staging':
      return t('updateModal.phaseStaging')
    case 'ready-restart':
      return t('updateModal.phaseReadyRestart')
    case 'failed':
      return p.error ? `${t('updateModal.phaseFailed')}：${p.error}` : t('updateModal.phaseFailed')
    default:
      return ''
  }
}

export default function UpdateModal({ open, onOpenChange, data, isLoading, isError, onRefresh }: UpdateModalProps) {
  const { t } = useTranslation()
  const { showError } = useMessage()
  const { status: connStatus } = useConnectionStatus()

  // 二次确认对话框开合
  const [confirmOpen, setConfirmOpen] = useState(false)
  // 是否已触发应用更新：触发后进入进度轮询 + 重启重连阶段
  const [applying, setApplying] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  // 重启窗口曾掉线：用于识别「重启后重连成功」边沿，回显新版本
  const wentOfflineRef = useRef(false)
  const [reconnected, setReconnected] = useState(false)

  // 触发应用后才轮询进度；未触发不打扰后端
  const { data: progress } = useQuery({
    queryKey: ['update-progress'],
    queryFn: updateProgress,
    enabled: applying,
    refetchInterval: applying ? PROGRESS_POLL_MS : false,
  })

  // 记录重启窗口的掉线 → 重连边沿：触发应用后控制面会短暂断连，重连成功即回显新版本。
  useEffect(() => {
    if (!applying) return
    if (connStatus === 'offline') wentOfflineRef.current = true
    if (wentOfflineRef.current && connStatus === 'online') setReconnected(true)
  }, [applying, connStatus])

  // 关闭模态框时复位本地态（下次打开干净）
  useEffect(() => {
    if (!open) {
      setApplying(false)
      setReconnected(false)
      wentOfflineRef.current = false
    }
  }, [open])

  // 「立即检查」：强制刷新
  async function handleRefresh() {
    setRefreshing(true)
    try {
      await onRefresh()
    } finally {
      setRefreshing(false)
    }
  }

  // 「确认更新」：POST 触发应用，202 后进入进度轮询 + 重启重连阶段；失败回显 error。
  async function handleConfirmUpdate() {
    setConfirmOpen(false)
    try {
      await triggerUpdate()
      setApplying(true)
    } catch (e) {
      const msg = e instanceof ApiClientError ? e.message : t('updateModal.triggerFailed')
      showError(msg)
    }
  }

  // 顶部状态行文案与是否可更新
  const canUpdate = data?.status === 'ok' && data.hasUpdate && !data.isDevBuild
  const failed = progress?.phase === 'failed'

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t('updateModal.title')}</DialogTitle>
            <DialogDescription>{statusLine()}</DialogDescription>
          </DialogHeader>

          {/* 版本 / 渠道 / 发布时间字段 */}
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
            <dt className="text-muted-foreground">{t('updateModal.currentVersion')}</dt>
            <dd className="font-medium tabular-nums">{data?.currentVersion ?? '-'}</dd>
            {data?.channel && (
              <>
                <dt className="text-muted-foreground">{t('updateModal.channel')}</dt>
                <dd className="font-medium">{data.channel}</dd>
              </>
            )}
            {canUpdate && (
              <>
                <dt className="text-muted-foreground">{t('updateModal.latestVersion')}</dt>
                <dd className="font-medium tabular-nums">{data?.latestVersion}</dd>
                <dt className="text-muted-foreground">{t('updateModal.publishedAt')}</dt>
                <dd className="font-medium">{formatTime(data?.publishedAt)}</dd>
              </>
            )}
          </dl>

          {/* release 日志：纯文本安全渲染（React 转义 + 保留换行），绝不注入 HTML */}
          {canUpdate && (
            <div className="space-y-1.5">
              <div className="text-sm font-medium">{t('updateModal.releaseNotes')}</div>
              <div className="max-h-48 overflow-y-auto rounded-md bg-muted/50 px-3 py-2 text-sm">
                {data?.releaseNotes ? (
                  <pre className="font-sans whitespace-pre-wrap break-words">{data.releaseNotes}</pre>
                ) : (
                  <span className="text-muted-foreground">{t('updateModal.releaseNotesEmpty')}</span>
                )}
              </div>
              {data?.releaseUrl && (
                <a
                  href={data.releaseUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 text-sm text-primary underline-offset-4 hover:underline"
                >
                  <ExternalLink className="size-3.5" />
                  {t('updateModal.releaseLink')}
                </a>
              )}
            </div>
          )}

          {/* 更新进度（触发应用后展示）：阶段 / 进度 / 重启重连 / 失败 */}
          {applying && (
            <div role="status" className="rounded-md border px-3 py-2 text-sm" data-failed={failed ? 'true' : 'false'}>
              {progressLine()}
            </div>
          )}

          {/* 操作区：立即检查（force） + 立即更新（二次确认） */}
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={handleRefresh} disabled={refreshing || applying}>
              <RefreshCw className={refreshing ? 'animate-spin' : undefined} />
              {t('updateModal.checkNow')}
            </Button>
            {canUpdate && (
              <Button size="sm" onClick={() => setConfirmOpen(true)} disabled={applying}>
                <Download />
                {t('updateModal.updateNow')}
              </Button>
            )}
          </div>
        </DialogContent>
      </Dialog>

      {/* 立即更新二次确认（复用 FR-76 破坏性确认范式）：提示重启 / 管理台不可用 / agent 按本地快照继续 */}
      <DestructiveConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('updateModal.confirmTitle', { version: data?.latestVersion ?? '' })}
        description={t('updateModal.confirmDesc')}
        confirmLabel={t('updateModal.confirmAction')}
        impacts={[
          t('updateModal.confirmImpactRestart'),
          t('updateModal.confirmImpactAgent'),
          t('updateModal.confirmImpactIrreversible'),
        ]}
        onConfirm={handleConfirmUpdate}
      />
    </>
  )

  // 触发应用后的进度行文案：重连成功优先回显新版本；进行中/失败按进度阶段；
  // 重启窗口断连（无进度可读）显示「正在重启重连」。
  function progressLine(): string {
    if (reconnected) {
      return t('updateModal.reconnected', { version: data?.latestVersion ?? data?.currentVersion ?? '' })
    }
    if (failed) return phaseText(t, progress!)
    // 控制面重启期间断连、进度端点不可达：显示重启重连提示
    if (connStatus === 'offline' || wentOfflineRef.current) return t('updateModal.restarting')
    if (progress) return phaseText(t, progress)
    return t('updateModal.phaseStaging')
  }

  // 顶部状态行：检查中 / 有更新 / 已最新 / 检查失败 / dev 构建
  function statusLine(): string {
    if (isLoading) return t('updateModal.checking')
    if (isError) return t('updateModal.checkFailed')
    if (!data) return t('updateModal.checking')
    if (data.isDevBuild) return t('updateModal.devBuild')
    if (data.status === 'check-failed') return t('updateModal.checkFailed')
    if (data.hasUpdate) return t('updateModal.hasUpdate', { version: data.latestVersion })
    return t('updateModal.upToDate')
  }
}
