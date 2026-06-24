// 密钥管理页（FR-42，见 ADR-0026）：列出 / 创建 / 重置 / 吊销管理面 API 密钥。
// 明文仅创建 / 重置时一次性展示——密钥只能重置、不能二次读取，丢失即重置轮换。
// 只读密钥仅供外部服务读 /admin/v1/*；管理台使用者恒为登录操作者（full），故本页不按角色裁剪。

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createApiKey, listApiKeys, resetApiKey, revokeApiKey } from '../api/client'
import type { ApiKeyCreated, ApiKeyView } from '../api/types'
import { formatTime } from '../api/format'
import { apiBaseFromLocation, buildApiKeyCurl } from '@/lib/curlCommand'
import { useMessage } from '../components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'
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
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import DestructiveConfirmDialog from '@/components/DestructiveConfirmDialog'

// 状态徽标配色：active 绿 / expired 琥珀 / revoked 灰
const STATUS_COLOR: Record<string, string> = {
  active: 'bg-green-600 text-white',
  expired: 'bg-amber-500 text-white',
  revoked: 'bg-muted text-muted-foreground',
}

// 状态文案经 i18n（apikeys.statusActive/Expired/Revoked）映射

// 把 datetime-local 本地值转成后端可识别的 ISO（UTC）字符串；空值返回 undefined
function toIso(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

export default function ApiKeysPage() {
  const { t } = useTranslation()
  // 密钥状态英文枚举 → 中文（i18n 映射，未知回退原文）
  const statusLabel = (status: string) =>
    t(`apikeys.status${status.charAt(0).toUpperCase()}${status.slice(1)}`, { defaultValue: status })
  const qc = useQueryClient()
  const msg = useMessage()

  // 新建表单
  const [name, setName] = useState('')
  const [role, setRole] = useState('readonly')
  const [expiresAt, setExpiresAt] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  // 一次性明文展示：非 null 时弹出（创建或重置后）
  const [revealed, setRevealed] = useState<ApiKeyCreated | null>(null)
  // 吊销 / 重置统一二次确认选中的密钥（null 表示关闭，FR-76）
  const [revoking, setRevoking] = useState<ApiKeyView | null>(null)
  const [resetting, setResetting] = useState<ApiKeyView | null>(null)

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['api-keys'],
    queryFn: listApiKeys,
  })

  const createMut = useMutation({
    mutationFn: () => createApiKey({ name: name.trim(), role, expiresAt: toIso(expiresAt) }),
    onSuccess: (created) => {
      setName('')
      setRole('readonly')
      setExpiresAt('')
      setCreateOpen(false)
      setRevealed(created)
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const resetMut = useMutation({
    mutationFn: (id: number) => resetApiKey(id),
    onSuccess: (created) => {
      setResetting(null)
      setRevealed(created)
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const revokeMut = useMutation({
    mutationFn: (id: number) => revokeApiKey(id),
    onSuccess: (_data, id) => {
      msg.showSuccess(t('apikeys.msgRevoked', { id }))
      setRevoking(null)
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!name.trim()) {
      msg.showError(t('apikeys.nameRequired'))
      return
    }
    createMut.mutate()
  }

  // 复制文本到剪贴板（明文 / curl 命令共用）
  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      msg.showSuccess(t('common.copiedToClipboard'))
    } catch {
      msg.showError(t('common.copyFailed'))
    }
  }

  // 密钥表列定义（操作列闭包引用 mutation，故在组件内定义）
  const columns: DataTableColumn<ApiKeyView>[] = [
    { header: t('apikeys.colName'), cell: (k) => k.name },
    {
      header: t('apikeys.colRole'),
      cell: (k) =>
        k.role === 'full' ? (
          <Badge>{t('apikeys.roleFull')}</Badge>
        ) : (
          <Badge variant="outline">{t('apikeys.roleReadonly')}</Badge>
        ),
    },
    { header: t('apikeys.colPrefix'), className: 'font-mono', cell: (k) => k.keyPrefix },
    {
      header: t('apikeys.colStatus'),
      cell: (k) => (
        <Badge className={cn('border-transparent', STATUS_COLOR[k.status])}>
          {statusLabel(k.status)}
        </Badge>
      ),
    },
    { header: t('apikeys.colCreatedAt'), cell: (k) => formatTime(k.createdAt) },
    { header: t('apikeys.colLastUsed'), cell: (k) => formatTime(k.lastUsedAt) },
    { header: t('apikeys.colExpiresAt'), cell: (k) => (k.expiresAt ? formatTime(k.expiresAt) : t('apikeys.expiresNever')) },
    {
      header: t('apikeys.colActions'),
      cell: (k) =>
        k.status === 'revoked' ? (
          <span className="text-sm text-muted-foreground">—</span>
        ) : (
          <div className="flex gap-2">
            {/* 重置：轮换明文，旧明文立即失效——开统一二次确认（FR-76） */}
            <Button
              variant="outline"
              size="sm"
              disabled={resetMut.isPending}
              onClick={() => setResetting(k)}
            >
              {t('apikeys.resetBtn')}
            </Button>
            {/* 吊销：软删，不可逆——开统一二次确认 + 手输密钥名复述高摩擦档（FR-76） */}
            <Button
              variant="destructive"
              size="sm"
              disabled={revokeMut.isPending}
              onClick={() => setRevoking(k)}
            >
              {t('apikeys.revokeBtn')}
            </Button>
          </div>
        ),
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t('apikeys.title')}</h1>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button>{t('apikeys.createBtn')}</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>{t('apikeys.createTitle')}</DialogTitle>
              <DialogDescription>
                {t('apikeys.createDesc')}
              </DialogDescription>
            </DialogHeader>
            <form id="create-api-key" onSubmit={onCreate} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="k-name">{t('apikeys.colName')}</Label>
                <Input
                  id="k-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={t('apikeys.namePlaceholder')}
                />
              </div>
              <div className="space-y-1.5">
                <Label>{t('common.role')}</Label>
                <Select value={role} onValueChange={setRole}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="readonly">{t('apikeys.roleReadonlyOpt')}</SelectItem>
                    <SelectItem value="full">{t('apikeys.roleFullOpt')}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="k-expires">{t('apikeys.expiresLabel')}</Label>
                <Input
                  id="k-expires"
                  type="datetime-local"
                  value={expiresAt}
                  onChange={(e) => setExpiresAt(e.target.value)}
                />
              </div>
            </form>
            <DialogFooter>
              <Button type="submit" form="create-api-key" disabled={createMut.isPending}>
                {t('apikeys.createSubmit')}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      <Card>
        <CardContent>
          <AsyncSection isLoading={isLoading} isError={isError} error={error}>
            <DataTable
              columns={columns}
              rows={data}
              rowKey={(k) => String(k.id)}
              emptyText={t('apikeys.empty')}
            />
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 一次性明文展示 Dialog：创建 / 重置后弹出，关闭后无法再次查看 */}
      <Dialog open={revealed !== null} onOpenChange={(open) => !open && setRevealed(null)}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t('apikeys.revealTitle')}</DialogTitle>
            <DialogDescription className="text-destructive">
              {t('apikeys.revealDesc')}
            </DialogDescription>
          </DialogHeader>
          {revealed && (
            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label>{t('apikeys.revealName')}</Label>
                <div className="text-sm">{revealed.name}</div>
              </div>
              <div className="space-y-1.5">
                <Label>{t('apikeys.revealKey')}</Label>
                <pre className="overflow-auto rounded-md border bg-muted p-3 font-mono text-sm break-all whitespace-pre-wrap">
                  {revealed.key}
                </pre>
              </div>
              <p className="text-xs text-muted-foreground">
                {t('apikeys.revealUsageBefore')}<code className="font-mono">X-Beacon-Api-Key</code>{t('apikeys.revealUsageOr')}
                <code className="font-mono">{t('apikeys.revealUsageBearer')}</code>{t('apikeys.revealUsageAfter')}
              </p>
            </div>
          )}
          <DialogFooter>
            {revealed && (
              <Button variant="outline" onClick={() => copyText(revealed.key)}>
                {t('apikeys.copyBtn')}
              </Button>
            )}
            {revealed && (
              // 复制为 curl（FR-90，见 ADR-0042）：拼带认证头、指向只读样例端点的可粘贴命令
              <Button
                variant="outline"
                onClick={() =>
                  copyText(buildApiKeyCurl(revealed.key, { base: apiBaseFromLocation() }))
                }
              >
                {t('apikeys.copyCurlBtn')}
              </Button>
            )}
            <Button onClick={() => setRevealed(null)}>{t('apikeys.savedBtn')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 重置密钥统一二次确认（FR-76）：带影响摘要，确认才轮换明文 */}
      <DestructiveConfirmDialog
        open={resetting !== null}
        onOpenChange={(o) => !o && setResetting(null)}
        title={resetting ? t('apikeys.resetConfirmTitle', { name: resetting.name }) : ''}
        description={t('apikeys.resetConfirmDescFlat')}
        impacts={[t('apikeys.resetImpactInvalidate'), t('apikeys.resetImpactExternal')]}
        confirmLabel={t('apikeys.resetConfirmAction')}
        pending={resetMut.isPending}
        onConfirm={() => resetting && resetMut.mutate(resetting.id)}
      />

      {/* 吊销密钥统一二次确认（FR-76）：带影响摘要 + 手输密钥名复述高摩擦档，确认才软删 */}
      <DestructiveConfirmDialog
        open={revoking !== null}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={revoking ? t('apikeys.revokeConfirmTitle', { name: revoking.name }) : ''}
        description={t('apikeys.revokeConfirmDescFlat')}
        impacts={[t('apikeys.revokeImpactInvalidate'), t('apikeys.revokeImpactExternal')]}
        confirmLabel={t('apikeys.revokeConfirmAction')}
        confirmPhrase={revoking?.name}
        pending={revokeMut.isPending}
        onConfirm={() => revoking && revokeMut.mutate(revoking.id)}
      />
    </div>
  )
}
