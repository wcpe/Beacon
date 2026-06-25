// 环境管理页：列出环境（namespace）+ 新建 / 改名 / 删除（FR-53）。
// 删除带后端守卫——环境下有实例 / zone / 配置 / 文件树 / 覆盖集时返 409，错误中文 message 直接提示。

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
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
import DestructiveConfirmDialog from '@/components/DestructiveConfirmDialog'

export default function NamespacesPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const msg = useMessage()
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  // 新建环境 Dialog 开关
  const [createOpen, setCreateOpen] = useState(false)
  // 改名 Dialog 选中的环境（null 表示关闭）；renameName 为草稿显示名
  const [renaming, setRenaming] = useState<NamespaceView | null>(null)
  const [renameName, setRenameName] = useState('')
  // 删除确认选中的环境（null 表示关闭，FR-76 统一二次确认）
  const [deleting, setDeleting] = useState<NamespaceView | null>(null)

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['namespaces'],
    queryFn: listNamespaces,
  })

  const createMut = useMutation({
    mutationFn: () => createNamespace(code.trim(), name.trim()),
    onSuccess: (ns) => {
      msg.showSuccess(t('namespaces.msgCreated', { code: ns.code }))
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
      msg.showSuccess(t('namespaces.msgRenamed', { code: ns.code }))
      setRenaming(null)
      qc.invalidateQueries({ queryKey: ['namespaces'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (c: string) => deleteNamespace(c),
    onSuccess: (_data, c) => {
      msg.showSuccess(t('namespaces.msgDeleted', { code: c }))
      setDeleting(null)
      qc.invalidateQueries({ queryKey: ['namespaces'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!code.trim() || !name.trim()) {
      msg.showError(t('namespaces.requiredFields'))
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
      msg.showError(t('namespaces.nameRequired'))
      return
    }
    renameMut.mutate({ code: renaming.code, name: renameName.trim() })
  }

  // 环境表列定义（操作列闭包引用 mutation / 状态，故在组件内定义）
  const columns: DataTableColumn<NamespaceView>[] = [
    { header: t('namespaces.colCode'), className: 'font-mono', cell: (ns) => ns.code },
    { header: t('namespaces.colName'), cell: (ns) => ns.name },
    {
      header: t('namespaces.colActions'),
      cell: (ns) => (
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => openRename(ns)}>
            {t('namespaces.renameBtn')}
          </Button>
          {/* 删除：开统一二次确认（FR-76）；后端守卫有在用数据（实例 / zone / 配置 / 文件树 / 覆盖集）时返 409，错误中文提示 */}
          <Button
            variant="destructive"
            size="sm"
            disabled={deleteMut.isPending}
            onClick={() => setDeleting(ns)}
          >
            {t('namespaces.deleteBtn')}
          </Button>
        </div>
      ),
    },
  ]

  return (
    <div className="space-y-6">
      {/* 独立页页眉（ADR-0048 拍平回独立路由）：页标题 + 右对齐新建入口 */}
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t('namespaces.title')}</h1>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button>{t('namespaces.createBtn')}</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>{t('namespaces.createTitle')}</DialogTitle>
            </DialogHeader>
            <form id="create-namespace" onSubmit={onCreate} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="n-code">{t('namespaces.colCode')}</Label>
                <Input
                  id="n-code"
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder={t('namespaces.codePlaceholder')}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="n-name">{t('namespaces.colName')}</Label>
                <Input
                  id="n-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={t('namespaces.namePlaceholder')}
                />
              </div>
            </form>
            <DialogFooter>
              <Button type="submit" form="create-namespace" disabled={createMut.isPending}>
                {t('namespaces.createSubmit')}
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
              emptyText={t('namespaces.empty')}
            />
          </AsyncSection>
        </CardContent>
      </Card>

      {/* 改名 Dialog：仅改显示名，code 不可变（只读展示） */}
      <Dialog open={renaming !== null} onOpenChange={(open) => !open && setRenaming(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t('namespaces.renameTitle')}</DialogTitle>
          </DialogHeader>
          {renaming && (
            <form id="rename-namespace" onSubmit={onRename} className="space-y-4">
              <div className="space-y-1.5">
                <Label htmlFor="rn-code">{t('namespaces.colCode')}</Label>
                <Input id="rn-code" value={renaming.code} disabled className="font-mono" />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="rn-name">{t('namespaces.colName')}</Label>
                <Input
                  id="rn-name"
                  value={renameName}
                  onChange={(e) => setRenameName(e.target.value)}
                  placeholder={t('namespaces.namePlaceholder')}
                />
              </div>
            </form>
          )}
          <DialogFooter>
            <Button type="submit" form="rename-namespace" disabled={renameMut.isPending}>
              {t('namespaces.renameSubmit')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除环境统一二次确认（FR-76）：带影响摘要（脱链哪层 / 影响哪些服），确认才删 */}
      <DestructiveConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={
          deleting
            ? t('namespaces.deleteConfirmTitle', { name: deleting.name, code: deleting.code })
            : ''
        }
        description={t('namespaces.deleteConfirmDescFlat')}
        impacts={[t('namespaces.deleteImpactData'), t('namespaces.deleteImpactServers')]}
        confirmLabel={t('namespaces.deleteConfirmAction')}
        pending={deleteMut.isPending}
        onConfirm={() => deleting && deleteMut.mutate(deleting.code)}
      />
    </div>
  )
}
