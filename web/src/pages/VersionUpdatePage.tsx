// 版本与更新独立页（FR-100，消费 FR-99 端点；ADR-0048 由设置子 tab 拍平为独立页）：
// 单张「应用更新」卡片承载版本信息 / 渠道分段控件（正式版 / 测试版）/ 检查更新 / 状态徽标 /
// release 日志（markdown 安全渲染）/ 立即更新并重启（二次确认 + 进度 + 重连，FR-118）/ 回滚到上一版本（FR-120）；
// 卡片下方「高级设置」折叠区承载网络代理（FR-98）与更新设置（自动检查 + 周期，FR-101）。
// 复用 useUpdateCheck（低频检查）+ FR-99 端点（triggerUpdate/updateProgress）+ 设置 store（update.* 项经 listSettings/updateSetting）。
//
// 安全渲染：releaseNotes 经 MarkdownLite 解析为 React 元素（文本由 React 转义），绝不用 dangerouslySetInnerHTML（防 XSS）。

import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ChevronDown, Download, ExternalLink, RefreshCw, Undo2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import MarkdownLite from '@/components/MarkdownLite'
import DestructiveConfirmDialog from '@/components/DestructiveConfirmDialog'
import { usePageHeader } from '@/components/PageHeader'
import { useMessage } from '@/components/useMessage'
import { useConnectionStatus } from '@/hooks/useConnectionStatus'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'
import { cn } from '@/lib/utils'
import {
  ApiClientError,
  listSettings,
  updateSetting,
  triggerUpdate,
  cancelUpdate,
  rollbackUpdate,
  updateProgress,
} from '@/api/client'
import { formatTime } from '@/api/format'
import type { SettingView, UpdateCheckView, UpdateProgressView } from '@/api/types'

// 进度轮询周期（毫秒）：触发应用后短周期反映 phase/percent，直到重启断连。
const PROGRESS_POLL_MS = 1500
// 渠道合法枚举（与后端 updateChannels 同口径，FR-117/ADR-0052：stable 正式版 / prerelease 滚动预发布）。
const UPDATE_CHANNELS = ['stable', 'prerelease'] as const
// 更新设置周期上下界（与后端白名单 [1,168] 一致）。
const MIN_INTERVAL_HOURS = 1
const MAX_INTERVAL_HOURS = 168

// 设置项 key（FR-98/FR-101）。
const KEY_CHANNEL = 'update.channel'
const KEY_PROXY = 'update.proxy-url'
const KEY_AUTO_CHECK = 'update.auto-check-enabled'
const KEY_INTERVAL = 'update.check-interval-hours'

// 从设置项列表取某 key 视图。
function findSetting(settings: SettingView[] | undefined, key: string): SettingView | undefined {
  return settings?.find((s) => s.key === key)
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

export default function VersionUpdatePage() {
  const { t } = useTranslation()
  const { showError, showSuccess } = useMessage()
  const qc = useQueryClient()
  const { status: connStatus } = useConnectionStatus()

  // 更新检查（低频）：当前版本 / 渠道 / 有无更新 / release 日志。
  const update = useUpdateCheck()
  const data = update.data

  // 设置项（与运维设置页同 ['settings'] 查询，react-query 去重）：取 update.* 四项。
  const { data: settings } = useQuery({ queryKey: ['settings'], queryFn: listSettings })

  // 本地草稿态
  const [refreshing, setRefreshing] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  // 回滚二次确认开合（FR-120）：与「立即更新」二次确认分开受控，复用同一 DestructiveConfirmDialog 范式。
  const [rollbackConfirmOpen, setRollbackConfirmOpen] = useState(false)
  const [applying, setApplying] = useState(false)
  // 高级设置折叠开合（默认折叠）：放网络代理 + 更新设置两块。
  const [advancedOpen, setAdvancedOpen] = useState(false)
  // 进行中操作类别（FR-120）：update（立即更新）/ rollback（回滚），共用 applying / 进度轮询 / 重连裁决机制，
  // 仅最终裁决 toast 文案按类别分流，避免重复一套重连边沿追踪逻辑。
  const [opKind, setOpKind] = useState<'update' | 'rollback'>('update')
  const wentOfflineRef = useRef(false)
  const [reconnected, setReconnected] = useState(false)
  // 最终裁决只播报一次（FR-118 ②）：成功重连 / 失败各自一次性 toast。
  const verdictShownRef = useRef(false)

  // 更新进度内存态：挂载即拉一次（取 rollbackAvailable 决定回滚按钮显隐，FR-120），
  // 仅在触发应用 / 回滚后才以短周期轮询进度（未进行操作时不高频打扰后端；该端点只读内存、不查库不打 GitHub）。
  const { data: progress } = useQuery({
    queryKey: ['update-progress'],
    queryFn: updateProgress,
    // 进行中才轮询；失败即停（不空转，进度区仍持续显失败原因，由按钮 !failed 条件支持重试，fix-1）。
    refetchInterval: (query) =>
      applying && query.state.data?.phase !== 'failed' ? PROGRESS_POLL_MS : false,
  })

  // 记录重启窗口的掉线 → 重连边沿：触发应用后控制面短暂断连，重连成功即回显新版本。
  useEffect(() => {
    if (!applying) return
    if (connStatus === 'offline') wentOfflineRef.current = true
    if (wentOfflineRef.current && connStatus === 'online') setReconnected(true)
  }, [applying, connStatus])

  const canUpdate = data?.status === 'ok' && data.hasUpdate && !data.isDevBuild
  // 是否存在可回退的上一版本（FR-120）：取自更新状态端点的 rollbackAvailable，决定「回滚到上一版本」按钮显隐。
  const canRollback = progress?.rollbackAvailable === true
  const failed = progress?.phase === 'failed'

  // 最终裁决 toast（FR-118 ②；FR-120 复用同一机制）：更新 / 回滚走完后明确成功 / 失败一次。
  // 成功 = 重连成功边沿（已下载 / 换版 / 重启 / 重连完成）；失败 = 进度 phase=failed。文案按 opKind 分流。
  useEffect(() => {
    if (!applying || verdictShownRef.current) return
    if (reconnected) {
      verdictShownRef.current = true
      if (opKind === 'rollback') {
        showSuccess(t('versionUpdate.rollbackSucceeded'))
      } else {
        showSuccess(
          t('updateModal.updateSucceeded', {
            version: data?.latestVersion || data?.currentVersion || '',
          }),
        )
      }
    } else if (failed) {
      verdictShownRef.current = true
      const reason = progress?.error || t('updateModal.phaseFailed')
      if (opKind === 'rollback') {
        showError(t('versionUpdate.rollbackFailed', { reason }))
      } else {
        showError(t('updateModal.updateFailed', { reason }))
      }
      // fix-1：失败后保留 applying（进度区持续显失败原因），仅靠按钮的 !failed 条件重新启用以便重试；
      // 进度轮询见 useQuery 的 refetchInterval——失败即停轮询，不空转。
    }
  }, [applying, opKind, reconnected, failed, progress, data, showSuccess, showError, t])

  // 当前渠道：优先取设置项（可改），回退检查结果回显渠道。
  const channelSetting = findSetting(settings, KEY_CHANNEL)
  const currentChannel = channelSetting?.value ?? data?.channel ?? 'stable'
  // 当前是否预发布渠道（决定是否显示「预发布」徽标）。
  const isPrerelease = currentChannel === 'prerelease'

  // 改渠道：写 update.channel 设置（热生效），成功后刷新设置 + 重新检查更新（切渠道后比对新渠道 release），
  // 并回显重检结果（FR-118 ③）：发现更新 / 已最新 / 检查失败，而非只「正在重新检查」。
  const channelMut = useMutation({
    mutationFn: (value: string) => updateSetting(KEY_CHANNEL, value),
    onSuccess: async (_res, channel) => {
      showSuccess(t('versionUpdate.channelSwitched'))
      qc.invalidateQueries({ queryKey: ['settings'] })
      // 切渠道后强制重查（绕服务端缓存），使版本比对落到新渠道，并据结果回显裁决。
      try {
        const result = (await update.refresh()) as UpdateCheckView | undefined
        if (!result || result.status === 'check-failed') {
          showError(t('versionUpdate.recheckFailed', { channel }))
        } else if (result.hasUpdate && !result.isDevBuild) {
          showSuccess(t('versionUpdate.recheckHasUpdate', { channel, version: result.latestVersion }))
        } else {
          showSuccess(t('versionUpdate.recheckUpToDate', { channel }))
        }
      } catch {
        showError(t('versionUpdate.recheckFailed', { channel }))
      }
    },
    onError: (e: Error) => showError(e.message),
  })

  // 渠道分段控件切换：仅在与当前渠道不同时写设置（避免重复点同一段触发空切换）。
  function handleChannelChange(value: string) {
    if (value === currentChannel) return
    channelMut.mutate(value)
  }

  // 「立即检查」：强制刷新（?force=true）。
  async function handleRefresh() {
    setRefreshing(true)
    try {
      await update.refresh()
    } finally {
      setRefreshing(false)
    }
  }

  // 「确认更新」：POST 触发应用（fix-1：后端已改异步、202 立即返回），点击即进 applying 态启动进度轮询；
  // 失败由进度轮询 phase=failed 经裁决 effect 回显脱敏原因并复位 applying 以便重试。
  async function handleConfirmUpdate() {
    setConfirmOpen(false)
    // 复位上一轮裁决 / 重连追踪，允许重试
    verdictShownRef.current = false
    wentOfflineRef.current = false
    setReconnected(false)
    setOpKind('update')
    setApplying(true)
    try {
      await triggerUpdate()
    } catch (e) {
      // 触发被拒（如 409 已有更新进行中）/ 网络失败 → 退出 applying，toast 出错误
      setApplying(false)
      const msg = e instanceof ApiClientError ? e.message : t('updateModal.triggerFailed')
      showError(msg)
    }
  }

  // 「停止」下载（FR-125）：取消进行中的更新下载，回到干净可重试态。后端进度回 idle（非 failed）。
  async function handleCancel() {
    try {
      await cancelUpdate()
      setApplying(false)
      wentOfflineRef.current = false
      setReconnected(false)
      verdictShownRef.current = false
      showSuccess(t('versionUpdate.updateCancelled'))
    } catch (e) {
      showError(e instanceof ApiClientError ? e.message : t('versionUpdate.cancelFailed'))
    }
  }

  // 「确认回滚」（FR-120）：POST 触发回滚，受理后复用「立即更新」的进度轮询 + 重启重连裁决机制（仅 opKind 标为 rollback）。
  // 后端 409 NO_ROLLBACK_AVAILABLE → 提示「无可回退的上一版本」；其余错误回显 message 或兜底文案。
  async function handleConfirmRollback() {
    setRollbackConfirmOpen(false)
    // 复位上一轮裁决 / 重连追踪，允许重试
    verdictShownRef.current = false
    wentOfflineRef.current = false
    setReconnected(false)
    setOpKind('rollback')
    setApplying(true)
    try {
      await rollbackUpdate()
    } catch (e) {
      setApplying(false)
      if (e instanceof ApiClientError) {
        const msg =
          e.code === 'NO_ROLLBACK_AVAILABLE' ? t('versionUpdate.rollbackNoneAvailable') : e.message
        showError(msg)
      } else {
        showError(t('versionUpdate.rollbackTriggerFailed'))
      }
    }
  }

  // 顶部状态行文案（与模态框口径一致）。
  function statusLine(): string {
    if (update.isLoading || !data) return t('updateModal.checking')
    if (update.isError) return t('updateModal.checkFailed')
    if (data.isDevBuild) return t('updateModal.devBuild')
    if (data.status === 'check-failed') return t('updateModal.checkFailed')
    if (data.hasUpdate) return t('updateModal.hasUpdate', { version: data.latestVersion })
    return t('updateModal.upToDate')
  }

  // 触发应用后进度行文案：重连成功优先回显新版本；进行中 / 失败按阶段；重启断连显示重连提示。
  function progressLine(): string {
    if (reconnected) {
      return t('updateModal.reconnected', { version: data?.latestVersion ?? data?.currentVersion ?? '' })
    }
    if (failed) return phaseText(t, progress!)
    if (connStatus === 'offline' || wentOfflineRef.current) return t('updateModal.restarting')
    if (progress) return phaseText(t, progress)
    return t('updateModal.phaseStaging')
  }

  // 状态徽标（卡片版本行旁）：有更新（warning 橙）/ 已最新（success 绿）；预发布渠道额外挂 accent 徽标。
  // 仅在检查就绪（非 dev / 非检查失败）时显示明确状态徽标，否则只靠状态行文字回显。
  function statusBadge() {
    if (!data || update.isLoading || update.isError) return null
    if (data.isDevBuild || data.status === 'check-failed') return null
    return canUpdate ? (
      <Badge className="border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400">
        {t('versionUpdate.badgeHasUpdate')}
      </Badge>
    ) : (
      <Badge className="border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400">
        {t('versionUpdate.badgeUpToDate')}
      </Badge>
    )
  }

  // 页眉（FR-105/FR-108）：标题 + 副标题；主操作下移卡片底部，系统页非环境范围。
  usePageHeader({
    title: t('versionUpdate.title'),
    subtitle: t('versionUpdate.subtitle'),
    envScoped: false,
  })

  return (
    <div className="flex h-full min-h-0 flex-col gap-4 overflow-y-auto pb-2">
      {/* ===== 应用更新单卡片 ===== */}
      <section>
        <Card>
          <CardHeader className="flex flex-wrap items-center justify-between gap-2 border-b pb-3">
            <CardTitle className="flex items-center gap-2">
              <Download className="size-4" />
              {t('versionUpdate.cardTitle')}
            </CardTitle>
            <div className="flex flex-wrap items-center justify-end gap-2">
              {/* 渠道分段控件（正式版 / 测试版）：映射 stable / prerelease */}
              <Tabs
                value={currentChannel}
                onValueChange={handleChannelChange}
                className="w-auto"
                aria-label={t('versionUpdate.channelGroupAria')}
              >
                <TabsList aria-label={t('versionUpdate.channelGroupAria')}>
                  {UPDATE_CHANNELS.map((c) => (
                    <TabsTrigger
                      key={c}
                      value={c}
                      disabled={channelMut.isPending || applying}
                    >
                      {c === 'stable'
                        ? t('versionUpdate.channelStable')
                        : t('versionUpdate.channelPrerelease')}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
              {/* 立即检查（强制刷新，绕服务端缓存） */}
              <Button variant="outline" size="sm" onClick={handleRefresh} disabled={refreshing || applying}>
                <RefreshCw className={refreshing ? 'animate-spin' : undefined} />
                {t('updateModal.checkNow')}
              </Button>
            </div>
          </CardHeader>

          <CardContent className="space-y-4">
            {/* 版本行：当前 / 最新（mono）/ 发布时间 + 状态徽标 */}
            <div className="space-y-2">
              <div className="flex flex-wrap items-center gap-2">
                {statusBadge()}
                {isPrerelease && (
                  <Badge className="bg-accent text-accent-foreground">
                    {t('versionUpdate.badgePrerelease')}
                  </Badge>
                )}
                <span className={canUpdate ? 'text-sm font-medium text-foreground' : 'text-sm text-muted-foreground'}>
                  {statusLine()}
                </span>
              </div>
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
                <dt className="text-muted-foreground">{t('updateModal.currentVersion')}</dt>
                <dd className="font-mono font-medium tabular-nums">{data?.currentVersion ?? '-'}</dd>
                {canUpdate && (
                  <>
                    <dt className="text-muted-foreground">{t('updateModal.latestVersion')}</dt>
                    <dd className="font-mono font-medium tabular-nums">{data?.latestVersion}</dd>
                    <dt className="text-muted-foreground">{t('updateModal.publishedAt')}</dt>
                    <dd className="font-medium">{formatTime(data?.publishedAt)}</dd>
                  </>
                )}
              </dl>
            </div>

            {/* 更新日志区（markdown 安全渲染）：仅有可用更新时展示 */}
            {canUpdate && (
              <div className="space-y-1.5">
                <div className="text-sm font-medium">{t('updateModal.releaseNotes')}</div>
                <div className="max-h-[180px] overflow-y-auto rounded-md bg-muted/50 px-3 py-2 text-sm break-words">
                  {data?.releaseNotes ? (
                    <MarkdownLite source={data.releaseNotes} />
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

            {/* 更新 / 回滚进度（触发应用后展示）：阶段 / 进度 / 重启重连 / 失败 + 下载中「停止」按钮（FR-125） */}
            {applying && (
              <div
                role="status"
                className="flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm"
                data-failed={failed ? 'true' : 'false'}
              >
                <span className="min-w-0 break-words">{progressLine()}</span>
                {/* 仅"下载进行中"（未失败、未进入重启重连）显示停止：取消下载、回到可重试态 */}
                {opKind === 'update' && !failed && !reconnected && !wentOfflineRef.current && (
                  <Button variant="outline" size="sm" className="shrink-0" onClick={handleCancel}>
                    {t('versionUpdate.cancelDownload')}
                  </Button>
                )}
              </div>
            )}
          </CardContent>

          {/* 卡片底部主操作：立即更新并重启（有更新时可用）+ 回滚到上一版本（仅有 .old 备份时显示，FR-120） */}
          <CardFooter className="gap-2">
            {/* 失败后（!failed 为假）重新启用以便重试：applying 仍为真使进度区持续显失败原因，但不锁死按钮（fix-1） */}
            <Button onClick={() => setConfirmOpen(true)} disabled={!canUpdate || (applying && !failed)}>
              <Download />
              {t('versionUpdate.applyAndRestart')}
            </Button>
            {canRollback && (
              <Button variant="outline" onClick={() => setRollbackConfirmOpen(true)} disabled={applying && !failed}>
                <Undo2 />
                {t('versionUpdate.rollback')}
              </Button>
            )}
          </CardFooter>
        </Card>
      </section>

      {/* ===== 高级设置折叠区（默认折叠）：网络代理 + 更新设置 ===== */}
      <section>
        <Card size="sm">
          <button
            type="button"
            className="flex w-full items-center justify-between px-(--card-spacing) text-sm font-medium"
            aria-expanded={advancedOpen}
            onClick={() => setAdvancedOpen((v) => !v)}
          >
            <span>{t('versionUpdate.advancedSettings')}</span>
            <ChevronDown className={cn('size-4 transition-transform', advancedOpen && 'rotate-180')} />
          </button>
          {advancedOpen && (
            <div className="space-y-6 px-(--card-spacing) pt-1">
              {/* 网络代理（FR-98） */}
              <div className="space-y-3">
                <div className="text-sm font-medium">{t('versionUpdate.sectionProxy')}</div>
                <ProxySection settings={settings} />
              </div>
              {/* 更新设置：自动检查开关 + 周期（FR-101） */}
              <div className="space-y-3">
                <div className="text-sm font-medium">{t('versionUpdate.sectionPrefs')}</div>
                <UpdatePrefsSection settings={settings} />
              </div>
            </div>
          )}
        </Card>
      </section>

      {/* 立即更新二次确认（复用 FR-76 破坏性确认范式） */}
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

      {/* 回滚到上一版本二次确认（FR-120，复用同一破坏性确认范式） */}
      <DestructiveConfirmDialog
        open={rollbackConfirmOpen}
        onOpenChange={setRollbackConfirmOpen}
        title={t('versionUpdate.rollbackConfirmTitle')}
        description={t('versionUpdate.rollbackConfirmDesc')}
        confirmLabel={t('versionUpdate.rollbackConfirmAction')}
        impacts={[
          t('versionUpdate.rollbackConfirmImpactRestart'),
          t('versionUpdate.rollbackConfirmImpactAgent'),
        ]}
        onConfirm={handleConfirmRollback}
      />
    </div>
  )
}

// 网络代理分区（FR-98）：编辑 update.proxy-url。
// 脱敏口径——GET 回脱敏串（如 http://***:***@host:port）；用户未改原样提交脱敏占位 → 后端保留原值不覆盖（见 settings_proxy_test）。
function ProxySection({ settings }: { settings: SettingView[] | undefined }) {
  const { t } = useTranslation()
  const { showError, showSuccess } = useMessage()
  const qc = useQueryClient()
  const item = findSetting(settings, KEY_PROXY)

  // 草稿：以服务端回显（脱敏）值为初值；服务端值变化（含保存后刷新）时同步未改动草稿。
  const [draft, setDraft] = useState('')
  const serverValue = item?.value ?? ''
  useEffect(() => {
    setDraft(serverValue)
  }, [serverValue])

  const mut = useMutation({
    mutationFn: (value: string) => updateSetting(KEY_PROXY, value),
    onSuccess: () => {
      showSuccess(t('settings.msgSaved'))
      qc.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: (e: Error) => showError(e.message),
  })

  // 项缺失（后端未含该设置）：兜底占位。
  if (!item) {
    return <p className="text-sm text-muted-foreground">{t('versionUpdate.proxyEmpty')}</p>
  }

  const dirty = draft !== serverValue

  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <Label htmlFor="update-proxy-url">{t('versionUpdate.proxyLabel')}</Label>
        <Input
          id="update-proxy-url"
          type="text"
          className="max-w-md"
          placeholder={t('versionUpdate.proxyPlaceholder')}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
        <p className="text-xs text-muted-foreground">{t('versionUpdate.proxyHint')}</p>
      </div>
      <Button size="sm" disabled={!dirty || mut.isPending} onClick={() => mut.mutate(draft)}>
        {mut.isPending ? t('settings.saving') : t('settings.saveBtn')}
      </Button>
    </div>
  )
}

// 更新设置分区（FR-101）：auto-check-enabled 开关 + check-interval-hours 数字（1-168）。
function UpdatePrefsSection({ settings }: { settings: SettingView[] | undefined }) {
  const { t } = useTranslation()
  const { showError, showSuccess } = useMessage()
  const qc = useQueryClient()

  const autoItem = findSetting(settings, KEY_AUTO_CHECK)
  const intervalItem = findSetting(settings, KEY_INTERVAL)

  // 周期草稿（开关即时保存、周期带保存按钮）。
  const [intervalDraft, setIntervalDraft] = useState('')
  const intervalServer = intervalItem?.value ?? ''
  useEffect(() => {
    setIntervalDraft(intervalServer)
  }, [intervalServer])

  const saveMut = useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) => updateSetting(key, value),
    onSuccess: () => {
      showSuccess(t('settings.msgSaved'))
      qc.invalidateQueries({ queryKey: ['settings'] })
    },
    onError: (e: Error) => showError(e.message),
  })

  if (!autoItem && !intervalItem) {
    return <p className="text-sm text-muted-foreground">{t('versionUpdate.prefsEmpty')}</p>
  }

  const autoEnabled = autoItem?.value === 'true'
  const intervalDirty = intervalDraft !== intervalServer

  return (
    <div className="space-y-4">
      {autoItem && (
        <label className="flex items-center gap-2 text-sm">
          <Checkbox
            checked={autoEnabled}
            disabled={saveMut.isPending}
            onCheckedChange={(v) =>
              saveMut.mutate({ key: KEY_AUTO_CHECK, value: v === true ? 'true' : 'false' })
            }
          />
          <span>{t('versionUpdate.autoCheckLabel')}</span>
          <span className="text-muted-foreground">
            {autoEnabled ? t('settings.boolOn') : t('settings.boolOff')}
          </span>
        </label>
      )}
      {intervalItem && (
        <div className="space-y-1.5">
          <Label htmlFor="update-interval-hours">{t('versionUpdate.intervalLabel')}</Label>
          <div className="flex items-center gap-3">
            <Input
              id="update-interval-hours"
              type="number"
              min={MIN_INTERVAL_HOURS}
              max={MAX_INTERVAL_HOURS}
              className="w-32"
              value={intervalDraft}
              onChange={(e) => setIntervalDraft(e.target.value)}
            />
            <Button
              size="sm"
              disabled={!intervalDirty || saveMut.isPending}
              onClick={() => saveMut.mutate({ key: KEY_INTERVAL, value: intervalDraft })}
            >
              {saveMut.isPending ? t('settings.saving') : t('settings.saveBtn')}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">{t('versionUpdate.intervalHint')}</p>
        </div>
      )}
    </div>
  )
}
