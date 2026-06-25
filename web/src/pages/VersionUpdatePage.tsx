// 版本与更新独立页（FR-100，消费 FR-99 端点；ADR-0048 由设置子 tab 拍平为独立页）：
// 纵向分区合并「版本信息 / 渠道选择 / 检查更新（含 release 日志安全渲染）/ 立即更新（二次确认 + 进度 + 重连）/
// 网络代理（脱敏回显）/ 更新设置（自动检查开关 + 周期）」。
// 复用 useUpdateCheck（低频检查）+ FR-99 端点（triggerUpdate/updateProgress）+ 设置 store（update.* 项经 listSettings/updateSetting）。
//
// 安全渲染：releaseNotes 作为文本子节点交 React 转义（whitespace-pre-wrap 保留换行），绝不用 dangerouslySetInnerHTML（防 XSS）。

import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ExternalLink, RefreshCw, Download } from 'lucide-react'

import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import DestructiveConfirmDialog from '@/components/DestructiveConfirmDialog'
import { useMessage } from '@/components/useMessage'
import { useConnectionStatus } from '@/hooks/useConnectionStatus'
import { useUpdateCheck } from '@/hooks/useUpdateCheck'
import {
  ApiClientError,
  listSettings,
  updateSetting,
  triggerUpdate,
  updateProgress,
} from '@/api/client'
import { formatTime } from '@/api/format'
import type { SettingView, UpdateProgressView } from '@/api/types'

// 进度轮询周期（毫秒）：触发应用后短周期反映 phase/percent，直到重启断连。
const PROGRESS_POLL_MS = 1500
// 渠道合法枚举（与后端 updateChannels / settingsEditing 同口径，FR-101）。
const UPDATE_CHANNELS = ['stable', 'rc'] as const
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
  const [applying, setApplying] = useState(false)
  const wentOfflineRef = useRef(false)
  const [reconnected, setReconnected] = useState(false)

  // 触发应用后才轮询进度；未触发不打扰后端。
  const { data: progress } = useQuery({
    queryKey: ['update-progress'],
    queryFn: updateProgress,
    enabled: applying,
    refetchInterval: applying ? PROGRESS_POLL_MS : false,
  })

  // 记录重启窗口的掉线 → 重连边沿：触发应用后控制面短暂断连，重连成功即回显新版本。
  useEffect(() => {
    if (!applying) return
    if (connStatus === 'offline') wentOfflineRef.current = true
    if (wentOfflineRef.current && connStatus === 'online') setReconnected(true)
  }, [applying, connStatus])

  const canUpdate = data?.status === 'ok' && data.hasUpdate && !data.isDevBuild
  const failed = progress?.phase === 'failed'

  // 当前渠道：优先取设置项（可改），回退检查结果回显渠道。
  const channelSetting = findSetting(settings, KEY_CHANNEL)
  const currentChannel = channelSetting?.value ?? data?.channel ?? 'stable'

  // 改渠道：写 update.channel 设置（热生效），成功后刷新设置 + 重新检查更新（切渠道后比对新渠道 release）。
  const channelMut = useMutation({
    mutationFn: (value: string) => updateSetting(KEY_CHANNEL, value),
    onSuccess: async () => {
      showSuccess(t('versionUpdate.channelSwitched'))
      qc.invalidateQueries({ queryKey: ['settings'] })
      // 切渠道后强制重查（绕服务端缓存），使版本比对落到新渠道。
      await update.refresh().catch(() => {})
    },
    onError: (e: Error) => showError(e.message),
  })

  // 「立即检查」：强制刷新（?force=true）。
  async function handleRefresh() {
    setRefreshing(true)
    try {
      await update.refresh()
    } finally {
      setRefreshing(false)
    }
  }

  // 「确认更新」：POST 触发应用，受理后进入进度轮询 + 重启重连阶段；失败回显 error。
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

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold">{t('versionUpdate.title')}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t('versionUpdate.subtitle')}</p>
      </div>

      {/* ===== 版本信息 + 渠道选择 + 检查 / 更新 ===== */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t('versionUpdate.sectionVersion')}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
            <dt className="text-muted-foreground">{t('updateModal.currentVersion')}</dt>
            <dd className="font-medium tabular-nums">{data?.currentVersion ?? '-'}</dd>
          </dl>

          {/* 渠道选择：用户可自由切 stable / rc（写设置热生效、切后重查） */}
          <div className="flex flex-wrap items-center gap-3">
            <Label className="text-sm text-muted-foreground">{t('updateModal.channel')}</Label>
            <Select
              value={currentChannel}
              onValueChange={(v) => channelMut.mutate(v)}
              disabled={channelMut.isPending || applying}
            >
              <SelectTrigger className="w-32" aria-label={t('updateModal.channel')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {UPDATE_CHANNELS.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <span className="text-xs text-muted-foreground">{t('versionUpdate.channelHint')}</span>
          </div>

          {/* 状态行 + 立即检查 / 立即更新 */}
          <div className="flex flex-wrap items-center gap-3">
            <span className={canUpdate ? 'text-sm font-medium text-foreground' : 'text-sm text-muted-foreground'}>
              {statusLine()}
            </span>
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

          {/* 可用更新明细：版本 / 发布时间 / release 日志（安全渲染）/ 外链 */}
          {canUpdate && (
            <div className="space-y-3">
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
                <dt className="text-muted-foreground">{t('updateModal.latestVersion')}</dt>
                <dd className="font-medium tabular-nums">{data?.latestVersion}</dd>
                <dt className="text-muted-foreground">{t('updateModal.publishedAt')}</dt>
                <dd className="font-medium">{formatTime(data?.publishedAt)}</dd>
              </dl>
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
            </div>
          )}

          {/* 更新进度（触发应用后展示）：阶段 / 进度 / 重启重连 / 失败 */}
          {applying && (
            <div role="status" className="rounded-md border px-3 py-2 text-sm" data-failed={failed ? 'true' : 'false'}>
              {progressLine()}
            </div>
          )}
        </CardContent>
      </Card>

      {/* ===== 网络代理 ===== */}
      <ProxySection settings={settings} />

      {/* ===== 更新设置（自动检查开关 + 周期） ===== */}
      <UpdatePrefsSection settings={settings} />

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
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t('versionUpdate.sectionProxy')}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">{t('versionUpdate.proxyEmpty')}</p>
        </CardContent>
      </Card>
    )
  }

  const dirty = draft !== serverValue

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t('versionUpdate.sectionProxy')}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
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
      </CardContent>
    </Card>
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
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t('versionUpdate.sectionPrefs')}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">{t('versionUpdate.prefsEmpty')}</p>
        </CardContent>
      </Card>
    )
  }

  const autoEnabled = autoItem?.value === 'true'
  const intervalDirty = intervalDraft !== intervalServer

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t('versionUpdate.sectionPrefs')}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
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
      </CardContent>
    </Card>
  )
}
