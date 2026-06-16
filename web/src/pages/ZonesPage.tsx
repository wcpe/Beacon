// zone 分配页：指派列表 + 新增/改派 + 取消指派 + zone 维度汇总。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  assignZone,
  listAssignments,
  unassignZone,
  zoneSummary,
} from '../api/client'
import type { AssignParams } from '../api/client'
import { formatTime } from '../api/format'
import { useOperator } from '../state/operator'
import MessageBar from '../components/MessageBar'
import { useMessage } from '../components/useMessage'

// 指派/汇总共用的过滤条件
interface ZoneFilter {
  namespace?: string
  group?: string
  zone?: string
}

export default function ZonesPage() {
  const qc = useQueryClient()
  const msg = useMessage()
  const [operator] = useOperator()

  // 过滤草稿与生效值
  const [fNamespace, setFNamespace] = useState('')
  const [fGroup, setFGroup] = useState('')
  const [fZone, setFZone] = useState('')
  const [filter, setFilter] = useState<ZoneFilter>({})

  // 新增/改派表单
  const [form, setForm] = useState({ namespace: '', serverId: '', group: '', zone: '', note: '' })

  const assignments = useQuery({
    queryKey: ['assignments', filter],
    queryFn: () => listAssignments(filter.namespace, filter.group, filter.zone),
  })

  const summary = useQuery({
    queryKey: ['zone-summary', filter.namespace, filter.group],
    queryFn: () => zoneSummary(filter.namespace, filter.group),
  })

  const assignMut = useMutation({
    mutationFn: (params: AssignParams) => assignZone(params),
    onSuccess: (a) => {
      msg.showSuccess(`已指派 ${a.serverId} → ${a.zone}`)
      setForm({ namespace: '', serverId: '', group: '', zone: '', note: '' })
      invalidate()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const unassignMut = useMutation({
    mutationFn: (vars: { namespace: string; serverId: string }) =>
      unassignZone(vars.namespace, vars.serverId, operator),
    onSuccess: (_d, vars) => {
      msg.showSuccess(`已取消 ${vars.serverId} 的指派`)
      invalidate()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function invalidate() {
    qc.invalidateQueries({ queryKey: ['assignments'] })
    qc.invalidateQueries({ queryKey: ['zone-summary'] })
  }

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: fNamespace.trim() || undefined,
      group: fGroup.trim() || undefined,
      zone: fZone.trim() || undefined,
    })
  }

  function onAssign(e: React.FormEvent) {
    e.preventDefault()
    if (!msg.requireOperator(operator)) return
    if (!form.namespace.trim() || !form.serverId.trim() || !form.group.trim() || !form.zone.trim()) {
      msg.showError('环境、serverId、大区、小区均为必填')
      return
    }
    assignMut.mutate({
      namespace: form.namespace.trim(),
      serverId: form.serverId.trim(),
      group: form.group.trim(),
      zone: form.zone.trim(),
      operator,
      note: form.note.trim(),
    })
  }

  function onUnassign(namespace: string, serverId: string) {
    if (!msg.requireOperator(operator)) return
    if (!window.confirm(`确认取消 ${serverId} 的 zone 指派？`)) return
    unassignMut.mutate({ namespace, serverId })
  }

  return (
    <div className="page">
      <h1>zone 分配</h1>
      <MessageBar message={msg.message} onClose={msg.clear} />

      <section className="panel">
        <h2>新增 / 改派</h2>
        <form className="form-grid" onSubmit={onAssign}>
          <label>
            环境
            <input value={form.namespace} onChange={(e) => setForm({ ...form, namespace: e.target.value })} />
          </label>
          <label>
            serverId
            <input value={form.serverId} onChange={(e) => setForm({ ...form, serverId: e.target.value })} />
          </label>
          <label>
            大区
            <input value={form.group} onChange={(e) => setForm({ ...form, group: e.target.value })} />
          </label>
          <label>
            小区
            <input value={form.zone} onChange={(e) => setForm({ ...form, zone: e.target.value })} />
          </label>
          <label>
            备注
            <input value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} />
          </label>
          <div className="form-actions">
            <button type="submit" disabled={assignMut.isPending}>
              指派
            </button>
          </div>
        </form>
      </section>

      <section className="panel">
        <h2>过滤</h2>
        <form className="form-grid" onSubmit={onSearch}>
          <label>
            环境
            <input value={fNamespace} onChange={(e) => setFNamespace(e.target.value)} />
          </label>
          <label>
            大区
            <input value={fGroup} onChange={(e) => setFGroup(e.target.value)} />
          </label>
          <label>
            小区
            <input value={fZone} onChange={(e) => setFZone(e.target.value)} />
          </label>
          <div className="form-actions">
            <button type="submit">查询</button>
          </div>
        </form>
      </section>

      <section className="panel">
        <h2>指派列表</h2>
        {assignments.isError && <p className="error-text">加载失败：{(assignments.error as Error).message}</p>}
        {assignments.isLoading ? (
          <p>加载中…</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>环境</th>
                <th>serverId</th>
                <th>大区</th>
                <th>小区</th>
                <th>备注</th>
                <th>更新时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {assignments.data && assignments.data.length > 0 ? (
                assignments.data.map((a) => (
                  <tr key={`${a.namespace}/${a.serverId}`}>
                    <td>{a.namespace}</td>
                    <td className="mono">{a.serverId}</td>
                    <td>{a.group}</td>
                    <td>{a.zone}</td>
                    <td>{a.note || '-'}</td>
                    <td>{formatTime(a.updatedAt)}</td>
                    <td>
                      <button
                        type="button"
                        className="btn-danger"
                        disabled={unassignMut.isPending}
                        onClick={() => onUnassign(a.namespace, a.serverId)}
                      >
                        取消指派
                      </button>
                    </td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={7} className="empty">
                    无指派记录
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        )}
      </section>

      <section className="panel">
        <h2>zone 汇总</h2>
        {summary.isError && <p className="error-text">加载失败：{(summary.error as Error).message}</p>}
        {summary.isLoading ? (
          <p>加载中…</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>大区</th>
                <th>小区</th>
                <th>服数</th>
                <th>在线数</th>
              </tr>
            </thead>
            <tbody>
              {summary.data && summary.data.length > 0 ? (
                summary.data.map((s) => (
                  <tr key={`${s.group}/${s.zone}`}>
                    <td>{s.group}</td>
                    <td>{s.zone}</td>
                    <td>{s.serverCount}</td>
                    <td>{s.onlineCount}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={4} className="empty">
                    无汇总数据
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
