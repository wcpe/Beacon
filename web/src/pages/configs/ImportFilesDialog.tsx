// 导入到组对话框（FR-38）：把一份本地目录 / 多文件批量上传到某组的文件树（scope=group）。
// 选环境 + 大区，选目录（webkitdirectory）或多文件（multiple）上传；成功后提示并失效文件缓存刷新文件树。
// 整文件覆盖语义复用通道B（FR-14）：同 path 已存在则发布新版本，否则首发。

import { useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { importFiles } from '../../api/client'
import type { ImportFileEntry } from '../../api/client'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'

// webkitdirectory 属性非标准 DOM 类型，单独声明以便在 input 上使用而不触发 TS 报错。
type DirInputProps = React.InputHTMLAttributes<HTMLInputElement> & {
  webkitdirectory?: string
  directory?: string
}

// 从浏览器 File 列表派生待导入条目：目录上传取 webkitRelativePath，多文件上传回退取文件名。
function toEntries(files: FileList | null): ImportFileEntry[] {
  if (!files) return []
  return Array.from(files).map((file) => ({
    path: file.webkitRelativePath || file.name,
    file,
  }))
}

export default function ImportFilesDialog({
  namespaces,
  groups,
}: {
  // 环境列表（来自 listNamespaces）
  namespaces: string[]
  // 大区列表（由 zone 汇总 / 实例派生）
  groups: string[]
}) {
  const qc = useQueryClient()
  const msg = useMessage()
  const [open, setOpen] = useState(false)
  const [namespace, setNamespace] = useState('')
  const [group, setGroup] = useState('')
  const [entries, setEntries] = useState<ImportFileEntry[]>([])

  // 打开时重置选择：环境缺省取列表首项，组与文件清空待选。
  useEffect(() => {
    if (open) {
      setNamespace(namespaces[0] ?? '')
      setGroup('')
      setEntries([])
    }
  }, [open, namespaces])

  const importMut = useMutation({
    mutationFn: () => importFiles(namespace, group, entries),
    onSuccess: (r) => {
      msg.showSuccess(`已导入 ${r.files} 个文件（新建 ${r.created}、更新 ${r.updated}）`)
      setOpen(false)
      // 失效文件相关缓存，刷新文件树
      qc.invalidateQueries({ queryKey: ['files'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onImport(e: React.FormEvent) {
    e.preventDefault()
    if (!namespace) {
      msg.showError('环境为必填')
      return
    }
    if (!group) {
      msg.showError('目标组为必填')
      return
    }
    if (entries.length === 0) {
      msg.showError('请先选择目录或文件')
      return
    }
    importMut.mutate()
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          导入到组
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>导入到组</DialogTitle>
        </DialogHeader>
        <form id="import-files" onSubmit={onImport} className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="imp-namespace">环境</Label>
            <select
              id="imp-namespace"
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
            >
              {namespaces.map((ns) => (
                <option key={ns} value={ns}>
                  {ns}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="imp-group">目标组</Label>
            <select
              id="imp-group"
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={group}
              onChange={(e) => setGroup(e.target.value)}
            >
              <option value="">请选择</option>
              {groups.map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </select>
          </div>
          <div className="col-span-2 space-y-1.5">
            <Label htmlFor="imp-dir">选择目录</Label>
            <input
              id="imp-dir"
              type="file"
              className="block w-full text-sm"
              {...({ webkitdirectory: '', directory: '', multiple: true } as DirInputProps)}
              onChange={(e) => setEntries(toEntries(e.target.files))}
            />
          </div>
          <div className="col-span-2 space-y-1.5">
            <Label htmlFor="imp-files">或选择多个文件</Label>
            <input
              id="imp-files"
              type="file"
              multiple
              className="block w-full text-sm"
              onChange={(e) => setEntries(toEntries(e.target.files))}
            />
          </div>
          {entries.length > 0 && (
            <p className="col-span-2 text-xs text-muted-foreground">已选 {entries.length} 个文件</p>
          )}
        </form>
        <DialogFooter>
          <Button type="submit" form="import-files" disabled={importMut.isPending}>
            {importMut.isPending ? '导入中…' : '导入'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
