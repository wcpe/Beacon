// 新建配置对话框：自管表单与新建 mutation，成功后失效配置列表缓存。
// 选项（环境 / 大区 / 小区 / 实例）由上层按动态数据传入，去硬编码示例；
// 覆盖目标随覆盖层联动（global 隐藏，group/zone/server 切换对应下拉）；
// 支持外部 open 受控与 initial 预填（「复制到实例」快捷路径复用本对话框）。

import { useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createConfig } from '../../api/client'
import type { CreateConfigParams } from '../../api/client'
import { useMessage } from '../../components/useMessage'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Combobox } from '@/components/ui/combobox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'

// 覆盖层选项（与后端 scopeLevel 约定一致）
const SCOPE_LEVELS = ['global', 'group', 'zone', 'server'] as const
// 格式选项
const FORMATS = ['yaml', 'properties', 'json'] as const

// 按动态数据生成新建表单初值（环境缺省取列表首项，无则空）
function emptyForm(namespaces: string[]): CreateConfigParams {
  return {
    namespace: namespaces[0] ?? '',
    group: '',
    dataId: '',
    scopeLevel: 'global',
    scopeTarget: '',
    format: 'yaml',
    content: '',
    comment: '',
  }
}

// 实例最小视图（仅取联动所需字段，避免与 api/types 强耦合）
export interface InstanceOption {
  serverId: string
  group: string
  zone: string | null
}

export default function CreateConfigDialog({
  namespaces,
  groups,
  zones,
  instances,
  open,
  onOpenChange,
  initial,
}: {
  // 环境列表（来自 listNamespaces）
  namespaces: string[]
  // 大区列表（由 zone 汇总 / 实例派生）
  groups: string[]
  // 小区列表（由 zone 汇总 / 实例派生）
  zones: string[]
  // 实例列表（server 层覆盖目标来源）
  instances: InstanceOption[]
  // 受控开合（上层持有，便于「复制到实例」外部唤起）
  open: boolean
  onOpenChange: (open: boolean) => void
  // 预填初值（「复制到实例」时注入源内容与 server 覆盖目标）
  initial?: CreateConfigParams
}) {
  const qc = useQueryClient()
  const msg = useMessage()
  const [form, setForm] = useState<CreateConfigParams>(() => initial ?? emptyForm(namespaces))

  // 打开时按 initial（或默认值）重置表单：复制路径每次唤起都拿到最新源内容。
  useEffect(() => {
    if (open) setForm(initial ?? emptyForm(namespaces))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, initial])

  const createMut = useMutation({
    mutationFn: (params: CreateConfigParams) => createConfig(params),
    onSuccess: (c) => {
      msg.showSuccess(`已新建配置 #${c.id}`)
      onOpenChange(false)
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
    if (form.scopeLevel !== 'global' && !form.scopeTarget.trim()) {
      msg.showError('覆盖目标为必填')
      return
    }
    createMut.mutate(form)
  }

  // 切换覆盖层：global 清空目标，其余切层时清空目标待重选（避免残留上一层的值）
  function onScopeLevelChange(level: string) {
    setForm({ ...form, scopeLevel: level, scopeTarget: '' })
  }

  // 当前覆盖层对应的目标候选值（group→大区，zone→小区，server→实例 serverId）
  const targetOptions =
    form.scopeLevel === 'group'
      ? groups
      : form.scopeLevel === 'zone'
        ? zones
        : form.scopeLevel === 'server'
          ? instances.map((i) => i.serverId)
          : []

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button size="sm">新建配置</Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>新建配置</DialogTitle>
        </DialogHeader>
        <form id="create-config" onSubmit={onCreate} className="grid grid-cols-2 gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="cc-namespace">环境</Label>
            {/* 环境严格选：须为已存在 namespace（FR-51） */}
            <Combobox
              id="cc-namespace"
              aria-label="环境"
              value={form.namespace}
              onChange={(v) => setForm({ ...form, namespace: v })}
              options={namespaces}
              allowCustom={false}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="cc-group">大区</Label>
            {/* 大区可编辑：可为尚未注册的新大区授权配置（FR-51）；留空表示 __GLOBAL__ */}
            <Combobox
              id="cc-group"
              aria-label="大区"
              value={form.group}
              onChange={(v) => setForm({ ...form, group: v })}
              options={groups}
              allowCustom
              placeholder="__GLOBAL__"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="cc-dataId">dataId</Label>
            <Input
              id="cc-dataId"
              value={form.dataId}
              onChange={(e) => setForm({ ...form, dataId: e.target.value })}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="cc-scopeLevel">覆盖层</Label>
            <select
              id="cc-scopeLevel"
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={form.scopeLevel}
              onChange={(e) => onScopeLevelChange(e.target.value)}
            >
              {SCOPE_LEVELS.map((lv) => (
                <option key={lv} value={lv}>
                  {lv}
                </option>
              ))}
            </select>
          </div>
          {/* 覆盖目标随覆盖层联动：global 不显示（全局无目标），其余为对应下拉。
              server 层目标须为已存在实例（严格选）；group/zone 目标可为新维度（可编辑，FR-51）。 */}
          {form.scopeLevel !== 'global' && (
            <div className="space-y-1.5">
              <Label htmlFor="cc-scopeTarget">覆盖目标</Label>
              <Combobox
                id="cc-scopeTarget"
                aria-label="覆盖目标"
                value={form.scopeTarget}
                onChange={(v) => setForm({ ...form, scopeTarget: v })}
                options={targetOptions}
                allowCustom={form.scopeLevel !== 'server'}
                placeholder="请选择"
              />
            </div>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="cc-format">格式</Label>
            <select
              id="cc-format"
              className="h-8 w-full rounded border border-input bg-background px-2 text-sm"
              value={form.format}
              onChange={(e) => setForm({ ...form, format: e.target.value })}
            >
              {FORMATS.map((f) => (
                <option key={f} value={f}>
                  {f}
                </option>
              ))}
            </select>
          </div>
          <div className="col-span-2 space-y-1.5">
            <Label htmlFor="cc-content">初始内容</Label>
            <Input
              id="cc-content"
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
