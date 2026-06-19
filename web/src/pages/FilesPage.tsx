// 文件树托管列表页（通道B，FR-14）：按 namespace/group/path/scopeLevel 过滤 + 新建文件（Dialog）。
// 支持「平铺表格 / 文件树」两种视图切换；两视图里点击文件均进入独立详情页 /files/:id（可深链）。

import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { ChevronDown, ChevronRight, File as FileIcon } from 'lucide-react'
import { createFile, listFiles } from '../api/client'
import type { CreateFileParams, FileFilter } from '../api/client'
import type { FileView } from '../api/types'
import { formatTime } from '../api/format'
import { useMessage } from '../components/useMessage'
import AsyncSection from '@/components/AsyncSection'
import DataTable, { type DataTableColumn } from '@/components/DataTable'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
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
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'

// 新建表单初值
const EMPTY_FORM = {
  namespace: '',
  group: '',
  path: '',
  scopeLevel: 'global',
  scopeTarget: '',
  content: '',
  comment: '',
}

// Radix Select 不允许空串值，"全部"用哨兵值 all 表示，提交时转 undefined
const ALL = 'all'

// 平铺表格列定义（无副作用，模块级；行点击导航交给 onRowClick）
const FLAT_COLUMNS: DataTableColumn<FileView>[] = [
  { header: 'ID', cell: (f) => f.id },
  { header: '环境', cell: (f) => f.namespace },
  { header: '大区', cell: (f) => f.group },
  { header: 'path', className: 'font-mono', cell: (f) => f.path },
  { header: '覆盖层', cell: (f) => f.scopeLevel },
  { header: '目标', cell: (f) => f.scopeTarget || '-' },
  { header: '版本', cell: (f) => f.version },
  { header: 'md5', className: 'font-mono', cell: (f) => f.md5.slice(0, 8) },
  { header: '状态', cell: (f) => (f.enabled ? '启用' : '已删') },
  { header: '更新时间', cell: (f) => formatTime(f.updatedAt) },
]

// 文件树节点：目录节点有 children，文件叶子节点带 file
interface TreeNode {
  name: string
  // 从根到本节点的完整路径（用于 React key 稳定）
  fullPath: string
  children: Map<string, TreeNode>
  file?: FileView
}

// 按 path 的 / 分段把平铺文件列表构建成目录树
function buildTree(files: FileView[]): TreeNode {
  const root: TreeNode = { name: '', fullPath: '', children: new Map() }
  for (const f of files) {
    const segments = f.path.split('/').filter((s) => s.length > 0)
    let node = root
    let acc = ''
    segments.forEach((seg, idx) => {
      acc = acc ? `${acc}/${seg}` : seg
      let child = node.children.get(seg)
      if (!child) {
        child = { name: seg, fullPath: acc, children: new Map() }
        node.children.set(seg, child)
      }
      // 最后一段是文件叶子，挂上原始文件对象
      if (idx === segments.length - 1) child.file = f
      node = child
    })
  }
  return root
}

export default function FilesPage() {
  const qc = useQueryClient()
  const msg = useMessage()
  const navigate = useNavigate()

  // 过滤草稿与生效值
  const [fNamespace, setFNamespace] = useState('')
  const [fGroup, setFGroup] = useState('')
  const [fPath, setFPath] = useState('')
  const [fScopeLevel, setFScopeLevel] = useState(ALL)
  const [filter, setFilter] = useState<FileFilter>({})

  // 新建表单与 Dialog 开关
  const [form, setForm] = useState(EMPTY_FORM)
  const [createOpen, setCreateOpen] = useState(false)

  const list = useQuery({
    queryKey: ['files', filter],
    queryFn: () => listFiles(filter),
  })

  // 文件树视图所需的树结构（按当前列表数据派生）
  const tree = useMemo(() => buildTree(list.data ?? []), [list.data])

  const createMut = useMutation({
    mutationFn: (params: CreateFileParams) => createFile(params),
    onSuccess: (f) => {
      msg.showSuccess(`已新建文件 #${f.id}（${f.path}）`)
      setForm(EMPTY_FORM)
      setCreateOpen(false)
      qc.invalidateQueries({ queryKey: ['files'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: fNamespace.trim() || undefined,
      group: fGroup.trim() || undefined,
      path: fPath.trim() || undefined,
      scopeLevel: fScopeLevel === ALL ? undefined : fScopeLevel,
    })
  }

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!form.namespace.trim() || !form.group.trim() || !form.path.trim()) {
      msg.showError('环境、大区、path 均为必填')
      return
    }
    createMut.mutate({
      namespace: form.namespace.trim(),
      group: form.group.trim(),
      path: form.path.trim(),
      scopeLevel: form.scopeLevel,
      scopeTarget: form.scopeTarget.trim(),
      content: form.content,
      comment: form.comment.trim(),
    })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">文件树托管</h1>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button>新建文件</Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-2xl">
            <DialogHeader>
              <DialogTitle>新建文件</DialogTitle>
            </DialogHeader>
            <form id="create-file" onSubmit={onCreate} className="grid grid-cols-2 gap-4">
              <div className="space-y-1.5">
                <Label htmlFor="f-namespace-new">环境</Label>
                <Input
                  id="f-namespace-new"
                  value={form.namespace}
                  onChange={(e) => setForm({ ...form, namespace: e.target.value })}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="f-group-new">大区</Label>
                <Input
                  id="f-group-new"
                  value={form.group}
                  onChange={(e) => setForm({ ...form, group: e.target.value })}
                  placeholder="global 层占位 __GLOBAL__"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="f-path-new">相对 path</Label>
                <Input
                  id="f-path-new"
                  value={form.path}
                  onChange={(e) => setForm({ ...form, path: e.target.value })}
                  placeholder="如 ui-components/main.allin"
                />
              </div>
              <div className="space-y-1.5">
                <Label>覆盖层</Label>
                <Select
                  value={form.scopeLevel}
                  onValueChange={(v) => setForm({ ...form, scopeLevel: v })}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="global">global</SelectItem>
                    <SelectItem value="group">group</SelectItem>
                    <SelectItem value="zone">zone</SelectItem>
                    <SelectItem value="server">server</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="f-target-new">覆盖目标</Label>
                <Input
                  id="f-target-new"
                  value={form.scopeTarget}
                  onChange={(e) => setForm({ ...form, scopeTarget: e.target.value })}
                  placeholder="zone/server 等层的目标键"
                />
              </div>
              <div className="col-span-2 space-y-1.5">
                <Label htmlFor="f-content-new">内容</Label>
                <Textarea
                  id="f-content-new"
                  rows={8}
                  className="font-mono"
                  value={form.content}
                  onChange={(e) => setForm({ ...form, content: e.target.value })}
                />
              </div>
              <div className="col-span-2 space-y-1.5">
                <Label htmlFor="f-comment-new">备注</Label>
                <Input
                  id="f-comment-new"
                  value={form.comment}
                  onChange={(e) => setForm({ ...form, comment: e.target.value })}
                />
              </div>
            </form>
            <DialogFooter>
              <Button type="submit" form="create-file" disabled={createMut.isPending}>
                创建并首次发布
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      <Card>
        <CardContent>
          <form onSubmit={onSearch} className="flex flex-wrap items-end gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="f-namespace">环境</Label>
              <Input id="f-namespace" value={fNamespace} onChange={(e) => setFNamespace(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-group">大区</Label>
              <Input id="f-group" value={fGroup} onChange={(e) => setFGroup(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="f-path">path</Label>
              <Input id="f-path" value={fPath} onChange={(e) => setFPath(e.target.value)} />
            </div>
            <div className="space-y-1.5">
              <Label>覆盖层</Label>
              <Select value={fScopeLevel} onValueChange={setFScopeLevel}>
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>全部</SelectItem>
                  <SelectItem value="global">global</SelectItem>
                  <SelectItem value="group">group</SelectItem>
                  <SelectItem value="zone">zone</SelectItem>
                  <SelectItem value="server">server</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button type="submit">查询</Button>
          </form>
        </CardContent>
      </Card>

      {list.isError && (
        <p className="text-sm text-destructive">加载失败：{(list.error as Error).message}</p>
      )}

      {/* 平铺表格 / 文件树 两视图切换 */}
      <Tabs defaultValue="flat">
        <TabsList>
          <TabsTrigger value="flat">平铺表格</TabsTrigger>
          <TabsTrigger value="tree">文件树</TabsTrigger>
        </TabsList>

        {/* 平铺视图：shadcn 表格 */}
        <TabsContent value="flat">
          <Card>
            <CardContent>
              <AsyncSection isLoading={list.isLoading}>
                <DataTable
                  columns={FLAT_COLUMNS}
                  rows={list.data}
                  rowKey={(f) => String(f.id)}
                  emptyText="无托管文件"
                  onRowClick={(f) => navigate(`/files/${f.id}`)}
                />
              </AsyncSection>
            </CardContent>
          </Card>
        </TabsContent>

        {/* 文件树视图：按 path 分段递归渲染，点击叶子进入详情 */}
        <TabsContent value="tree">
          <Card>
            <CardContent>
              {list.isLoading ? (
                <p className="text-sm text-muted-foreground">加载中…</p>
              ) : list.data && list.data.length > 0 ? (
                <div className="font-mono text-sm">
                  {[...tree.children.values()].map((node) => (
                    <TreeRow key={node.fullPath} node={node} depth={0} onPick={(id) => navigate(`/files/${id}`)} />
                  ))}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">无托管文件</p>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  )
}

// 单个树节点行：目录可折叠/展开，文件叶子可点击进入详情
function TreeRow({
  node,
  depth,
  onPick,
}: {
  node: TreeNode
  depth: number
  onPick: (id: number) => void
}) {
  const isLeaf = node.file !== undefined
  const [open, setOpen] = useState(true)

  // 按层级缩进（每层 16px）
  const indentStyle = { paddingLeft: `${depth * 16 + 8}px` }

  if (isLeaf) {
    const f = node.file!
    return (
      <div
        className="flex cursor-pointer items-center gap-1.5 rounded py-1 pr-2 hover:bg-muted"
        style={indentStyle}
        onClick={() => onPick(f.id)}
      >
        {/* 叶子无折叠箭头，用占位对齐目录的箭头宽度 */}
        <span className="inline-block w-3.5 shrink-0" />
        <FileIcon className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="text-primary">{node.name}</span>
        <span className="ml-2 text-xs text-muted-foreground">
          {f.scopeLevel}
          {f.scopeTarget ? ` / ${f.scopeTarget}` : ''} · v{f.version} · {f.enabled ? '启用' : '已删'}
        </span>
      </div>
    )
  }

  return (
    <div>
      <div
        className="flex cursor-pointer items-center gap-1.5 rounded py-1 pr-2 hover:bg-muted"
        style={indentStyle}
        onClick={() => setOpen((v) => !v)}
      >
        {open ? (
          <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
        )}
        <span className={cn('text-foreground', !open && 'opacity-80')}>{node.name}/</span>
      </div>
      {open &&
        [...node.children.values()].map((child) => (
          <TreeRow key={child.fullPath} node={child} depth={depth + 1} onPick={onPick} />
        ))}
    </div>
  )
}
