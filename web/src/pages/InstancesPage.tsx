// 实例与健康页：按 namespace/group/zone/role/status 过滤，5 秒轮询健康。
// online/lost/offline 三色区分；未分配 zone（zone 为 null）的行高亮；支持手动下线。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { listInstances, offlineInstance } from '../api/client'
import type { InstanceFilter } from '../api/client'
import { formatTime } from '../api/format'
import MessageBar from '../components/MessageBar'
import StatusBadge from '../components/StatusBadge'
import { useMessage } from '../components/useMessage'

// 健康轮询周期（毫秒）
const REFETCH_MS = 5000

export default function InstancesPage() {
  const qc = useQueryClient()
  const msg = useMessage()

  const [namespace, setNamespace] = useState('')
  const [group, setGroup] = useState('')
  const [zone, setZone] = useState('')
  const [role, setRole] = useState('')
  const [status, setStatus] = useState('')
  const [filter, setFilter] = useState<InstanceFilter>({})

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['instances', filter],
    queryFn: () => listInstances(filter),
    refetchInterval: REFETCH_MS,
  })

  const offlineMut = useMutation({
    mutationFn: (serverId: string) => {
      const ns = filter.namespace
      if (!ns) throw new Error('下线需要先在过滤条件中指定环境')
      return offlineInstance(serverId, ns)
    },
    onSuccess: (_data, serverId) => {
      msg.showSuccess(`已下线实例 ${serverId}`)
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: namespace.trim() || undefined,
      group: group.trim() || undefined,
      zone: zone.trim() || undefined,
      role: role.trim() || undefined,
      status: status.trim() || undefined,
    })
  }

  function onOffline(serverId: string) {
    if (!filter.namespace) {
      msg.showError('请先在过滤条件中指定环境（下线接口需要 namespace）')
      return
    }
    if (!window.confirm(`确认下线实例 ${serverId}？`)) return
    offlineMut.mutate(serverId)
  }

  return (
    <div className="page">
      <h1>
        实例与健康 {isFetching && <span className="muted">（刷新中…）</span>}
      </h1>
      <MessageBar message={msg.message} onClose={msg.clear} />

      <section className="panel">
        <form className="form-grid" onSubmit={onSearch}>
          <label>
            环境
            <input value={namespace} onChange={(e) => setNamespace(e.target.value)} />
          </label>
          <label>
            大区
            <input value={group} onChange={(e) => setGroup(e.target.value)} />
          </label>
          <label>
            小区
            <input value={zone} onChange={(e) => setZone(e.target.value)} />
          </label>
          <label>
            角色
            <select value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="">全部</option>
              <option value="bukkit">bukkit</option>
              <option value="bungee">bungee</option>
            </select>
          </label>
          <label>
            状态
            <select value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">全部</option>
              <option value="online">online</option>
              <option value="lost">lost</option>
              <option value="offline">offline</option>
            </select>
          </label>
          <div className="form-actions">
            <button type="submit">查询</button>
          </div>
        </form>
        <p className="hint">提示：手动下线需在上方过滤条件中指定「环境」。未分配小区的实例以黄色高亮。</p>
      </section>

      <section className="panel">
        {isError && <p className="error-text">加载失败：{(error as Error).message}</p>}
        {isLoading ? (
          <p>加载中…</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>serverId</th>
                <th>环境</th>
                <th>角色</th>
                <th>大区</th>
                <th>小区</th>
                <th>状态</th>
                <th>地址</th>
                <th>版本</th>
                <th>人数</th>
                <th>TPS</th>
                <th>最近心跳</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {data && data.length > 0 ? (
                data.map((i) => (
                  <tr key={`${i.namespace}/${i.serverId}`} className={i.zone === null ? 'row-unassigned' : ''}>
                    <td className="mono">{i.serverId}</td>
                    <td>{i.namespace}</td>
                    <td>{i.role}</td>
                    <td>{i.group}</td>
                    <td>{i.zone === null ? <span className="badge badge-warn">未分配</span> : i.zone}</td>
                    <td>
                      <StatusBadge status={i.status} />
                    </td>
                    <td className="mono">{i.address}</td>
                    <td>{i.version}</td>
                    <td>{i.playerCount}</td>
                    <td>{i.tps.toFixed(1)}</td>
                    <td>{formatTime(i.lastHeartbeat)}</td>
                    <td>
                      <button
                        type="button"
                        className="btn-danger"
                        disabled={offlineMut.isPending}
                        onClick={() => onOffline(i.serverId)}
                      >
                        下线
                      </button>
                    </td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={12} className="empty">
                    无在册实例
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        )}
      </section>
    </div>
  )
}
