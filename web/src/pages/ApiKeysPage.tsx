// 密钥管理页（FR-42，见 ADR-0026）：列出 / 创建 / 重置 / 吊销管理面 API 密钥。
// 明文仅创建 / 重置时一次性展示——密钥只能重置、不能二次读取，丢失即重置轮换。
// 只读密钥仅供外部服务读 /admin/v1/*；管理台使用者恒为登录操作者（full），故本页不按角色裁剪。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createApiKey, listApiKeys, resetApiKey, revokeApiKey } from '../api/client'
import type { ApiKeyCreated, ApiKeyView } from '../api/types'
import { formatTime } from '../api/format'
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
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'

// 状态徽标配色：active 绿 / expired 琥珀 / revoked 灰
const STATUS_COLOR: Record<string, string> = {
  active: 'bg-green-600 text-white',
  expired: 'bg-amber-500 text-white',
  revoked: 'bg-muted text-muted-foreground',
}

// 状态中文文案
const STATUS_LABEL: Record<string, string> = {
  active: '生效',
  expired: '已过期',
  revoked: '已吊销',
}

// 把 datetime-local 本地值转成后端可识别的 ISO（UTC）字符串；空值返回 undefined
function toIso(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

export default function ApiKeysPage() {
  const qc = useQueryClient()
  const msg = useMessage()

  // 新建表单
  const [name, setName] = useState('')
  const [role, setRole] = useState('readonly')
  const [expiresAt, setExpiresAt] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  // 一次性明文展示：非 null 时弹出（创建或重置后）
  const [revealed, setRevealed] = useState<ApiKeyCreated | null>(null)

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
      setRevealed(created)
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const revokeMut = useMutation({
    mutationFn: (id: number) => revokeApiKey(id),
    onSuccess: (_data, id) => {
      msg.showSuccess(`已吊销密钥 #${id}`)
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!name.trim()) {
      msg.showError('密钥名称为必填')
      return
    }
    createMut.mutate()
  }

  // 复制明文到剪贴板
  async function copyKey(key: string) {
    try {
      await navigator.clipboard.writeText(key)
      msg.showSuccess('已复制到剪贴板')
    } catch {
      msg.showError('复制失败，请手动选中复制')
    }
  }

  // 密钥表列定义（操作列闭包引用 mutation，故在组件内定义）
  const columns: DataTableColumn<ApiKeyView>[] = [
    { header: '名称', cell: (k) => k.name },
    {
      header: '角色',
      cell: (k) =>
        k.role === 'full' ? (
          <Badge>full（读写）</Badge>
        ) : (
          <Badge variant="outline">readonly（只读）</Badge>
        ),
    },
    { header: '前缀', className: 'font-mono', cell: (k) => k.keyPrefix },
    {
      header: '状态',
      cell: (k) => (
        <Badge className={cn('border-transparent', STATUS_COLOR[k.status])}>
          {STATUS_LABEL[k.status] ?? k.status}
        </Badge>
      ),
    },
    { header: '创建时间', cell: (k) => formatTime(k.createdAt) },
    { header: '最近使用', cell: (k) => formatTime(k.lastUsedAt) },
    { header: '过期时间', cell: (k) => (k.expiresAt ? formatTime(k.expiresAt) : '永不') },
    {
      header: '操作',
      cell: (k) =>
        k.status === 'revoked' ? (
          <span className="text-sm text-muted-foreground">—</span>
        ) : (
          <div className="flex gap-2">
            {/* 重置：轮换明文，旧明文立即失效 */}
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="outline" size="sm" disabled={resetMut.isPending}>
                  重置
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>重置密钥「{k.name}」？</AlertDialogTitle>
                  <AlertDialogDescription>
                    将生成一把新明文，<strong>旧明文立即失效</strong>，使用旧明文的外部服务需更新为新值。密钥不能二次读取，请在重置后立即保存新明文。
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>取消</AlertDialogCancel>
                  <AlertDialogAction onClick={() => resetMut.mutate(k.id)}>确认重置</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
            {/* 吊销：软删，不可逆 */}
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="destructive" size="sm" disabled={revokeMut.isPending}>
                  吊销
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>吊销密钥「{k.name}」？</AlertDialogTitle>
                  <AlertDialogDescription>
                    吊销后该密钥<strong>立即失效且不可恢复</strong>，使用它的外部服务将无法再访问。如需继续访问请另建新密钥。
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>取消</AlertDialogCancel>
                  <AlertDialogAction onClick={() => revokeMut.mutate(k.id)}>确认吊销</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        ),
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">密钥管理</h1>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button>新建密钥</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>新建 API 密钥</DialogTitle>
              <DialogDescription>
                供外部服务调 /admin/v1/* 使用。只读密钥仅可读、不可写。明文仅创建后展示一次。
              </DialogDescription>
            </DialogHeader>
            <form id="create-api-key" onSubmit={onCreate} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="k-name">名称</Label>
                <Input
                  id="k-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="如 业务管理后端"
                />
              </div>
              <div className="space-y-1.5">
                <Label>角色</Label>
                <Select value={role} onValueChange={setRole}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="readonly">readonly（只读，仅可读端点）</SelectItem>
                    <SelectItem value="full">full（读写，等同操作者）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="k-expires">过期时间（可选，留空永不过期）</Label>
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
                创建
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
              emptyText="暂无密钥"
            />
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 一次性明文展示 Dialog：创建 / 重置后弹出，关闭后无法再次查看 */}
      <Dialog open={revealed !== null} onOpenChange={(open) => !open && setRevealed(null)}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>密钥已生成：请立即保存</DialogTitle>
            <DialogDescription className="text-destructive">
              这是该密钥明文唯一一次展示，关闭后无法再查看。如遗失只能重置（轮换）后获取新明文。
            </DialogDescription>
          </DialogHeader>
          {revealed && (
            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label>密钥名称</Label>
                <div className="text-sm">{revealed.name}</div>
              </div>
              <div className="space-y-1.5">
                <Label>明文密钥</Label>
                <pre className="overflow-auto rounded-md border bg-muted p-3 font-mono text-sm break-all whitespace-pre-wrap">
                  {revealed.key}
                </pre>
              </div>
              <p className="text-xs text-muted-foreground">
                经请求头 <code className="font-mono">X-Beacon-Api-Key</code> 或{' '}
                <code className="font-mono">Authorization: Bearer &lt;明文&gt;</code> 携带使用。
              </p>
            </div>
          )}
          <DialogFooter>
            {revealed && (
              <Button variant="outline" onClick={() => copyKey(revealed.key)}>
                复制明文
              </Button>
            )}
            <Button onClick={() => setRevealed(null)}>我已保存</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
