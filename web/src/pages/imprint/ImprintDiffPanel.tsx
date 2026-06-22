// 拓印 diff 面板（FR-46）：展示本地实际值 ⟷ 期望合并值（Monaco DiffEditor + FR-45 逐键来源徽标），
// 选并入层（global/group/zone/server）+ 预览将入库内容 → 单人自审确认（带 diff 返回的 actualMd5）。
// 自审门由 reviewedMd5 实现：确认必带 diff 拉到的 actualMd5，盲确认拿不到正确 md5 → 后端 412 拒。

import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { confirmImprint, imprintDiff } from '../../api/client'
import type { ImprintScope } from '../../api/types'
import type { InstanceView } from '../../api/types'
import { useMessage } from '../../components/useMessage'
import CodeEditor from '../../components/CodeEditor'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'

// 并入层选项：四层覆盖（global 不需目标键，group 需大区，zone/server 需大区 + 目标键）。
const SCOPE_OPTIONS: Array<{ value: ImprintScope; label: string }> = [
  { value: 'server', label: '子服层' },
  { value: 'zone', label: '小区层' },
  { value: 'group', label: '大区层' },
  { value: 'global', label: '全局层' },
]

export default function ImprintDiffPanel({
  commandId,
  serverId,
  sourceGroup,
  groups,
  instances,
  onConfirmed,
}: {
  // 已就绪（ready）的拓印命令 id
  commandId: number
  // 拓印源 serverId（确认落 server 层时的缺省目标键）
  serverId: string
  // 拓印源所属大区（并入层 group 缺省）
  sourceGroup: string
  // 大区候选（并入层目标组）
  groups: string[]
  // 实例候选（server 层目标键）
  instances: InstanceView[]
  // 确认落库成功回调（清理上层状态）
  onConfirmed: () => void
}) {
  const qc = useQueryClient()
  const msg = useMessage()
  // 并入层选择（缺省落回拓印源子服层）
  const [scope, setScope] = useState<ImprintScope>('server')
  const [group, setGroup] = useState(sourceGroup)
  const [zone, setZone] = useState('')
  const [target, setTarget] = useState(serverId)

  // 源信息变化时同步缺省（切换待审命令时复位）
  useEffect(() => {
    setGroup(sourceGroup)
    setTarget(serverId)
    setScope('server')
    setZone('')
  }, [sourceGroup, serverId, commandId])

  // diff 随并入层视角实时解析（期望合并值按所选 group/zone 解出）
  const diffQuery = useQuery({
    queryKey: ['imprint-diff', commandId, scope, group, zone, target],
    queryFn: () =>
      imprintDiff(commandId, {
        scope,
        group: scope === 'global' ? undefined : group,
        zone: scope === 'zone' ? zone : undefined,
        target: scope === 'server' ? target : undefined,
      }),
  })
  const diff = diffQuery.data

  const confirmMut = useMutation({
    // 自审门：必带 diff 返回的 actualMd5（看过 diff 才有该值）
    mutationFn: () =>
      confirmImprint(commandId, {
        scope,
        group: scope === 'global' ? undefined : group,
        zone: scope === 'zone' ? zone : undefined,
        target: scope === 'server' ? target : undefined,
        reviewedMd5: diff?.actualMd5 ?? '',
      }),
    onSuccess: (res) => {
      msg.showSuccess(`已确认同步：落 ${res.scopeLevel} 层（version ${res.version}）`)
      // 失效文件相关缓存：落库后文件树会变更
      qc.invalidateQueries({ queryKey: ['files'] })
      qc.invalidateQueries({ queryKey: ['file-effective'] })
      onConfirmed()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 并入层目标键是否齐备（zone/server 必选目标键，group 必选组）
  const targetReady = useMemo(() => {
    if (scope === 'global') return true
    if (scope === 'group') return !!group
    if (scope === 'zone') return !!group && !!zone
    return !!group && !!target // server
  }, [scope, group, zone, target])

  function onConfirm() {
    if (!diff) {
      msg.showError('diff 尚未就绪')
      return
    }
    if (!targetReady) {
      msg.showError('请补全并入层的目标键')
      return
    }
    confirmMut.mutate()
  }

  return (
    <div className="flex flex-1 flex-col min-h-0 gap-2">
      {/* 并入层选择条 */}
      <div className="flex flex-wrap items-end gap-3 px-3 py-2 border-b border-border bg-muted/20">
        <div className="space-y-1">
          <Label htmlFor="imp-scope" className="text-xs">
            并入层
          </Label>
          <select
            id="imp-scope"
            className="h-8 w-28 rounded border border-input bg-background px-2 text-sm"
            value={scope}
            onChange={(e) => setScope(e.target.value as ImprintScope)}
          >
            {SCOPE_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </div>
        {scope !== 'global' && (
          <div className="space-y-1">
            <Label htmlFor="imp-group" className="text-xs">
              大区
            </Label>
            <select
              id="imp-group"
              className="h-8 w-32 rounded border border-input bg-background px-2 text-sm"
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
        )}
        {scope === 'zone' && (
          <div className="space-y-1">
            <Label htmlFor="imp-zone" className="text-xs">
              小区编码
            </Label>
            <input
              id="imp-zone"
              className="h-8 w-32 rounded border border-input bg-background px-2 text-sm"
              value={zone}
              onChange={(e) => setZone(e.target.value)}
              placeholder="zone 编码"
            />
          </div>
        )}
        {scope === 'server' && (
          <div className="space-y-1">
            <Label htmlFor="imp-target" className="text-xs">
              目标子服
            </Label>
            <select
              id="imp-target"
              className="h-8 w-40 rounded border border-input bg-background px-2 text-sm"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
            >
              <option value="">请选择</option>
              {instances.map((i) => (
                <option key={i.serverId} value={i.serverId}>
                  {i.serverId}（{i.group}）
                </option>
              ))}
            </select>
          </div>
        )}
        <div className="ml-auto flex items-center gap-2">
          {diff &&
            (diff.differs ? (
              <Badge variant="destructive" className="text-xs">
                有差异
              </Badge>
            ) : (
              <Badge variant="secondary" className="text-xs">
                无差异
              </Badge>
            ))}
          <Button size="sm" onClick={onConfirm} disabled={confirmMut.isPending || !diff}>
            {confirmMut.isPending ? '同步中…' : '确认同步'}
          </Button>
        </div>
      </div>

      {/* diff 主体：左期望合并值、右本地实际值 */}
      {diffQuery.isLoading ? (
        <div className="flex-1 flex items-center justify-center text-sm text-muted-foreground">
          解析 diff 中…
        </div>
      ) : diffQuery.isError ? (
        <div className="flex-1 flex items-center justify-center text-sm text-destructive">
          {(diffQuery.error as Error).message}
        </div>
      ) : diff ? (
        <div className="flex flex-1 flex-col min-h-0">
          <div className="flex items-center gap-3 px-3 py-1 text-xs text-muted-foreground border-b border-border">
            <span className="font-mono break-all">{diff.path}</span>
            <span>
              左：期望合并值（{diff.expectedMd5.slice(0, 8) || '空'}）· 右：本地实际值（将入库，
              {diff.actualMd5.slice(0, 8)}）
            </span>
          </div>
          <div className="flex-1 min-h-0 border-b border-border">
            <CodeEditor
              original={diff.expectedContent}
              modified={diff.actualContent}
              language={langOf(diff.path)}
            />
          </div>
          {/* 期望合并值逐键来源（复用 FR-45 provenance 徽标） */}
          {diff.expectedSources.length > 0 && (
            <div className="px-3 py-1.5 bg-muted/10 text-[0.7rem]">
              <span className="text-muted-foreground">
                {diff.expectedWholeFile ? '期望整文件来自：' : '期望合并值来源：'}
              </span>
              {diff.expectedSources.map((src, idx) => (
                <span key={idx} className="ml-1.5 text-blue-600">
                  {src.path.length > 0 ? `${src.path.join('.')} (${src.scope})` : src.scope}
                </span>
              ))}
            </div>
          )}
        </div>
      ) : null}
    </div>
  )
}

// 按文件后缀推断 Monaco 语言（yaml/json，余者纯文本）。
function langOf(path: string): string {
  if (path.endsWith('.json')) return 'json'
  if (path.endsWith('.yml') || path.endsWith('.yaml')) return 'yaml'
  return 'plaintext'
}
