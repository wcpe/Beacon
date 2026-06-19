// 新建配置对话框：自管表单与新建 mutation，成功后失效配置列表缓存。

import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createConfig } from '../../api/client'
import type { CreateConfigParams } from '../../api/client'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'

// 新建表单初值
const EMPTY_FORM: CreateConfigParams = {
  namespace: 'prod',
  group: '__GLOBAL__',
  dataId: '',
  scopeLevel: 'global',
  scopeTarget: '',
  format: 'yaml',
  content: '',
  comment: '',
}

export default function CreateConfigDialog() {
  const qc = useQueryClient()
  const msg = useMessage()
  const [form, setForm] = useState<CreateConfigParams>(EMPTY_FORM)
  const [open, setOpen] = useState(false)

  const createMut = useMutation({
    mutationFn: (params: CreateConfigParams) => createConfig(params),
    onSuccess: (c) => {
      msg.showSuccess(`已新建配置 #${c.id}`)
      setForm(EMPTY_FORM)
      setOpen(false)
      qc.invalidateQueries({ queryKey: ['configs'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!form.dataId.trim()) {
      msg.showError('dataId 为必填')
      return
    }
    createMut.mutate(form)
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">新建配置</Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>新建配置</DialogTitle>
        </DialogHeader>
        <form id="create-config" onSubmit={onCreate} className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label>环境</Label>
            <select
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={form.namespace}
              onChange={(e) => setForm({ ...form, namespace: e.target.value })}
            >
              <option value="prod">prod</option>
              <option value="test">test</option>
            </select>
          </div>
          <div className="space-y-1.5">
            <Label>大区</Label>
            <select
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={form.group}
              onChange={(e) => setForm({ ...form, group: e.target.value })}
            >
              <option value="__GLOBAL__">__GLOBAL__</option>
              <option value="server-a">server-a</option>
              <option value="server-b">server-b</option>
            </select>
          </div>
          <div className="space-y-1.5">
            <Label>dataId</Label>
            <Input value={form.dataId} onChange={(e) => setForm({ ...form, dataId: e.target.value })} />
          </div>
          <div className="space-y-1.5">
            <Label>覆盖层</Label>
            <select
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={form.scopeLevel}
              onChange={(e) => setForm({ ...form, scopeLevel: e.target.value })}
            >
              <option value="global">global</option>
              <option value="group">group</option>
              <option value="zone">zone</option>
              <option value="server">server</option>
            </select>
          </div>
          <div className="space-y-1.5">
            <Label>覆盖目标</Label>
            <Input
              value={form.scopeTarget}
              onChange={(e) => setForm({ ...form, scopeTarget: e.target.value })}
            />
          </div>
          <div className="space-y-1.5">
            <Label>格式</Label>
            <select
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={form.format}
              onChange={(e) => setForm({ ...form, format: e.target.value })}
            >
              <option value="yaml">yaml</option>
              <option value="properties">properties</option>
              <option value="json">json</option>
            </select>
          </div>
          <div className="col-span-2 space-y-1.5">
            <Label>初始内容</Label>
            <Input
              value={form.content}
              onChange={(e) => setForm({ ...form, content: e.target.value })}
              placeholder="可选"
            />
          </div>
        </form>
        <DialogFooter>
          <Button type="submit" form="create-config" disabled={createMut.isPending}>
            创建
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
