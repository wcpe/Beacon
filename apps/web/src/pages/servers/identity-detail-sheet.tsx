// 身份详情抽屉：复用 /servers 既有抽屉交互，只展示控制面身份事实，绝不展示 token。
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'

import { AsyncSection, Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@beacon/ui'

import { fetchIdentityDetail, setIdentityEndpointOverride } from '../../api/cluster'
import ReasonDialog from './reason-dialog'

interface IdentityDetailSheetProps {
  identityId: string | null
  onOpenChange: (open: boolean) => void
}

export default function IdentityDetailSheet({ identityId, onOpenChange }: IdentityDetailSheetProps) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState<{ endpointKey: string; clear: boolean } | null>(null)
  const [overrideAddress, setOverrideAddress] = useState('')
  const [expandedInactiveEndpoints, setExpandedInactiveEndpoints] = useState<string[]>([])
  const query = useQuery({
    queryKey: ['identity-detail', identityId],
    queryFn: () => fetchIdentityDetail(identityId ?? ''),
    enabled: identityId !== null,
  })
  const detail = query.data
  const updateEndpoint = useMutation({
    mutationFn: ({ endpointKey, override, reason }: { endpointKey: string; override: string | null; reason: string }) =>
      setIdentityEndpointOverride(identityId ?? '', endpointKey, override, reason),
    onSuccess: async () => {
      setEditing(null)
      setOverrideAddress('')
      await queryClient.invalidateQueries({ queryKey: ['identity-detail', identityId] })
    },
  })
  const editedEndpoint = detail?.endpoints.find((endpoint) => endpoint.endpointKey === editing?.endpointKey) ?? null

  return (
    <Sheet open={identityId !== null} onOpenChange={onOpenChange} modal={false}>
      <SheetContent showOverlay={false} className="w-full gap-0 overflow-y-auto data-[side=right]:sm:max-w-[min(32rem,90vw)]">
        <SheetHeader>
          <SheetTitle>身份详情</SheetTitle>
          <SheetDescription className="font-mono">{shortIdentityId(identityId)}</SheetDescription>
        </SheetHeader>
        <div className="px-4 pb-6">
          <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
            {detail && (
              <dl className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
                <IdentityField label="服务器 ID" value={detail.serverId ?? '待分配服务器 ID'} mono />
                <IdentityField label="命名空间" value={String(detail.namespaceId)} mono />
                <IdentityField label="类型" value={detail.kind === 'proxy' ? '代理' : '子服'} />
                <IdentityField label="Agent 版本" value={detail.agentVersion ?? '未提供'} mono />
                <IdentityField label="工作目录" value={detail.serverWorkDir ?? '未上报（旧 agent）'} mono />
                <IdentityField label="来源地址" value={detail.lastAddr ?? '未提供'} mono />
                <IdentityField label="绑定来源" value={bindingSourceLabel(detail.bindingSource)} />
                <IdentityField label="迁移状态" value={migrationStateLabel(detail.migrationState)} />
                <IdentityField label="旧绑定迁移时间" value={formatTime(detail.legacyMigratedAt)} />
                <IdentityField label="最近权威确认" value={formatTime(detail.boundAt)} />
                <IdentityField label="绑定指纹摘要" value={summarizeFingerprint(detail.bindingFingerprint)} mono />
              </dl>
            )}
            {detail && (
              <section className="mt-4 rounded-xl border border-border bg-card p-4 shadow-card" aria-label="监听地址">
                <h3 className="text-sm font-medium text-ink-1">监听地址（{detail.endpoints.length}）</h3>
                {detail.endpoints.length === 0 ? (
                  <p className="mt-2 text-sm text-ink-4">尚未上报监听地址</p>
                ) : (
                  <div className="mt-3 grid gap-3">
                    {detail.endpoints.map((endpoint) => {
                      const expanded = endpoint.active || expandedInactiveEndpoints.includes(endpoint.endpointKey)

                      return (
                        <div key={endpoint.endpointKey} className="rounded-lg border border-border/70 p-3">
                        <div className="flex items-center justify-between gap-2">
                          <p className="font-mono text-sm text-ink-1">{endpointLabel(detail.kind, endpoint.ordinal)}</p>
                          <span className={endpoint.active ? 'text-xs text-emerald-600' : 'text-xs text-ink-4'}>
                            {endpoint.active ? '活跃' : '已失活'}
                          </span>
                        </div>
                        <p className="mt-1 text-xs text-ink-4"><span>最后上报时间</span>：{formatTime(endpoint.lastSeenAt)}</p>
                        {!endpoint.active && (
                          <button
                            type="button"
                            className="mt-2 rounded-md border border-border px-2.5 py-1 text-xs"
                            onClick={() => {
                              setExpandedInactiveEndpoints((keys) =>
                                expanded ? keys.filter((key) => key !== endpoint.endpointKey) : [...keys, endpoint.endpointKey],
                              )
                            }}
                          >
                            {expanded ? '收起已失活监听' : '展开已失活监听'}
                          </button>
                        )}
                        {expanded && <dl className="mt-2 grid gap-2">
                          <IdentityField label="上报监听" value={`${endpoint.reportedBindHost}:${String(endpoint.reportedPort)}`} mono />
                          <IdentityField label="自动探测地址" value={endpoint.detectedAddress} mono />
                          <IdentityField label="管理覆盖地址" value={endpoint.overrideAddress ?? '未设置'} mono />
                          <IdentityField label="当前有效地址" value={endpoint.effectiveAddress} mono />
                          <IdentityField label="地址来源" value={endpoint.source === 'override' ? '管理覆盖' : '自动探测地址'} />
                        </dl>}
                        {expanded && <div className="mt-3 flex gap-2">
                          <button
                            type="button"
                            className="rounded-md border border-border px-2.5 py-1 text-xs disabled:cursor-not-allowed disabled:opacity-50"
                            disabled={!endpoint.active}
                            onClick={() => {
                              setOverrideAddress(endpoint.overrideAddress ?? '')
                              setEditing({ endpointKey: endpoint.endpointKey, clear: false })
                            }}
                          >
                            设置覆盖
                          </button>
                          {endpoint.overrideAddress !== null && (
                            <button
                              type="button"
                              className="rounded-md border border-border px-2.5 py-1 text-xs"
                              onClick={() => {
                                setEditing({ endpointKey: endpoint.endpointKey, clear: true })
                              }}
                            >
                              清除覆盖
                            </button>
                          )}
                        </div>}
                        </div>
                      )
                    })}
                  </div>
                )}
              </section>
            )}
          </AsyncSection>
        </div>
      </SheetContent>
      <ReasonDialog
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open) {
            setEditing(null)
          }
        }}
        title={editing?.clear ? '清除地址覆盖' : '设置地址覆盖'}
        description={editing?.clear ? '清除后将恢复控制面自动探测地址。' : '覆盖仅作用于当前监听地址，必须填写原因。'}
        confirmLabel={editing?.clear ? '清除覆盖' : '保存覆盖'}
        pending={updateEndpoint.isPending}
        errorText={updateEndpoint.error?.message ?? null}
        confirmDisabled={editing !== null && !editing.clear && overrideAddress.trim() === ''}
        onConfirm={(reason) => {
          if (editing === null || editedEndpoint === null) {
            return
          }
          updateEndpoint.mutate({
            endpointKey: editedEndpoint.endpointKey,
            override: editing.clear ? null : overrideAddress.trim(),
            reason,
          })
        }}
      >
        {editing !== null && !editing.clear && (
          <label className="grid gap-1.5 text-sm text-ink-2">
            覆盖地址
            <input
              aria-label="覆盖地址"
              className="h-9 rounded-md border border-input bg-background px-3 font-mono text-sm"
              value={overrideAddress}
              onChange={(event) => {
                setOverrideAddress(event.target.value)
              }}
              placeholder="例如 203.0.113.20:25565"
            />
          </label>
        )}
      </ReasonDialog>
    </Sheet>
  )
}

function endpointLabel(kind: string, ordinal: number): string {
  return kind === 'proxy' ? `BC listener ${String(ordinal + 1)}` : 'Bukkit 主地址'
}

function IdentityField({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid gap-1 border-b border-border/70 pb-3 last:border-b-0 last:pb-0">
      <dt className="text-[11px] font-medium tracking-wide text-ink-4">{label}</dt>
      <dd className={mono ? 'break-all font-mono text-sm text-ink-1' : 'text-sm text-ink-1'}>{value}</dd>
    </div>
  )
}

function shortIdentityId(identityId: string | null): string {
  if (identityId === null) {
    return '未提供'
  }
  return identityId.length <= 16 ? identityId : `${identityId.slice(0, 8)}…${identityId.slice(-6)}`
}

function bindingSourceLabel(source: string | null): string {
  if (source === 'admin_assigned') {
    return '管理端分配'
  }
  if (source === 'legacy_local') {
    return '旧本地配置迁移'
  }
  // FR-235：控制面经机器注册通道替服预置的占位身份（尚无真 agent 接入），
  // 真 agent 启动并获批后会自动接管该 serverId。
  if (source === 'machine_registered') {
    return '控制面预置'
  }
  return '未提供'
}

function migrationStateLabel(state: string | null): string {
  if (state === 'pending') {
    return '迁移待完成'
  }
  if (state === 'completed') {
    return '迁移已完成'
  }
  if (state === 'not_required') {
    return '无需迁移'
  }
  return '未提供'
}

function formatTime(value: string | null): string {
  return value === null ? '未提供' : new Date(value).toLocaleString()
}

// 仅显示摘要，避免把完整指纹铺满抽屉；指纹本身不含 token。
function summarizeFingerprint(value: string | null): string {
  if (value === null) {
    return '未提供'
  }
  return value.length <= 24 ? value : `${value.slice(0, 16)}…${value.slice(-8)}`
}
