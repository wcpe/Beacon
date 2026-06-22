// 环境管理页：列出环境（namespace）+ 新建 / 改名 / 删除（FR-53）。
// 删除带后端守卫——环境下有实例 / zone / 配置时返 409，错误中文 message 直接提示。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  createNamespace,
  deleteNamespace,
  listNamespaces,
  updateNamespace,
} from '../api/client'
import type { NamespaceView } from '../api/types'
import { useMessage } from '../components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
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

export default function NamespacesPage() {
  const qc = useQueryClient()
  const msg = useMessage()
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  // 新建环境 Dialog 开关
  const [createOpen, setCreateOpen] = useState(false)
  // 改名 Dialog 选中的环境（null 表示关闭）；renameName 为草稿显示名
  const [renaming, setRenaming] = useState<NamespaceView | null>(null)
  const [renameName, setRenameName] = useState('')

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['namespaces'],
    queryFn: listNamespaces,
  })

  const createMut = useMutation({
    mutationFn: () => createNamespace(code.trim(), name.trim()),
    onSuccess: (ns) => {
      msg.showSuccess(`已新建环境 ${ns.code}`)
      setCode('')
      setName('')
      setCreateOpen(false)
      qc.invalidateQueries({ queryKey: ['namespaces'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const renameMut = useMutation({
    mutationFn: (vars: { code: string; name: string }) => updateNamespace(vars.code, vars.name),
    onSuccess: (ns) => {
      msg.showSuccess(`已更新环境 ${ns.code} 的名称`)
      setRenaming(null)
      qc.invalidateQueries({ queryKey: ['namespaces'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (c: string) => deleteNamespace(c),
    onSuccess: (_data, c) => {
      msg.showSuccess(`已删除环境 ${c}`)
      qc.invalidateQueries({ queryKey: ['namespaces'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!code.trim() || !name.trim()) {
      msg.showError('环境编码与名称均为必填')
      return
    }
    createMut.mutate()
  }

  function openRename(ns: NamespaceView) {
    setRenaming(ns)
    setRenameName(ns.name)
  }

  function onRename(e: React.FormEvent) {
    e.preventDefault()
    if (!renaming) return
    if (!renameName.trim()) {
      msg.showError('环境名称为必填')
      return
    }
    renameMut.mutate({ code: renaming.code, name: renameName.trim() })
  }

  // 环境表列定义（操作列闭包引用 mutation / 状态，故在组件内定义）
  const columns: DataTableColumn<NamespaceView>[] = [
    { header: '编码', className: 'font-mono', cell: (ns) => ns.code },
    { header: '名称', cell: (ns) => ns.name },
    {
      header: '操作',
      cell: (ns) => (
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => openRename(ns)}>
            改名
          </Button>
          {/* 删除：二次确认；后端守卫有在用数据时返 409，错误中文提示 */}
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button variant="destructive" size="sm" disabled={deleteMut.isPending}>
                删除
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>删除环境「{ns.name}」（{ns.code}）？</AlertDialogTitle>
                <AlertDialogDescription>
                  删除后不可恢复。若该环境下仍有<strong>已注册实例 / 已指派 zone / 配置</strong>，
                  将被禁止删除并提示原因——请先清理后再删。
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>取消</AlertDialogCancel>
                <AlertDialogAction onClick={() => deleteMut.mutate(ns.code)}>确认删除</AlertDialogAction>
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
        <h1 className="text-xl font-semibold">环境管理</h1>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button>新建环境</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>新建环境</DialogTitle>
            </DialogHeader>
            <form id="create-namespace" onSubmit={onCreate} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="n-code">编码</Label>
                <Input
                  id="n-code"
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder="如 prod"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="n-name">名称</Label>
                <Input
                  id="n-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="如 生产环境"
                />
              </div>
            </form>
            <DialogFooter>
              <Button type="submit" form="create-namespace" disabled={createMut.isPending}>
                新建
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
              rowKey={(ns) => ns.code}
              emptyText="暂无环境"
            />
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 改名 Dialog：仅改显示名，code 不可变（只读展示） */}
      <Dialog open={renaming !== null} onOpenChange={(open) => !open && setRenaming(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>改环境名称</DialogTitle>
          </DialogHeader>
          {renaming && (
            <form id="rename-namespace" onSubmit={onRename} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="rn-code">编码</Label>
                <Input id="rn-code" value={renaming.code} disabled className="font-mono" />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="rn-name">名称</Label>
                <Input
                  id="rn-name"
                  value={renameName}
                  onChange={(e) => setRenameName(e.target.value)}
                  placeholder="如 生产环境"
                />
              </div>
            </form>
          )}
          <DialogFooter>
            <Button type="submit" form="rename-namespace" disabled={renameMut.isPending}>
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
