// 审计日志页：按 namespace/action/targetType/targetRef/时间范围过滤，分页展示（时间倒序）。

import { useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { listAudits } from '../api/client'
import type { AuditFilter } from '../api/client'
import { formatTime } from '../api/format'

// 单页条数（固定，运维场景无需可配）
const PAGE_SIZE = 20

// 把表单里的本地时间值转成后端可识别的 ISO（UTC）字符串；空值返回 undefined
function toIso(local: string): string | undefined {
  if (!local) return undefined
  const d = new Date(local)
  if (Number.isNaN(d.getTime())) return undefined
  return d.toISOString()
}

export default function AuditsPage() {
  // 过滤表单的草稿值（点「查询」时才生效）
  const [namespace, setNamespace] = useState('')
  const [action, setAction] = useState('')
  const [targetType, setTargetType] = useState('')
  const [targetRef, setTargetRef] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  // 已生效的查询条件
  const [filter, setFilter] = useState<AuditFilter>({ page: 1, size: PAGE_SIZE })

  const { data, isLoading, isError, error, isFetching } = useQuery({
    queryKey: ['audits', filter],
    queryFn: () => listAudits(filter),
    placeholderData: keepPreviousData,
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: namespace.trim() || undefined,
      action: action.trim() || undefined,
      targetType: targetType.trim() || undefined,
      targetRef: targetRef.trim() || undefined,
      from: toIso(from),
      to: toIso(to),
      page: 1,
      size: PAGE_SIZE,
    })
  }

  function goPage(page: number) {
    setFilter((f) => ({ ...f, page }))
  }

  const total = data?.total ?? 0
  const page = filter.page ?? 1
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  return (
    <div className="page">
      <h1>审计日志</h1>

      <section className="panel">
        <form className="form-grid" onSubmit={onSearch}>
          <label>
            环境
            <input value={namespace} onChange={(e) => setNamespace(e.target.value)} />
          </label>
          <label>
            动作
            <input value={action} onChange={(e) => setAction(e.target.value)} placeholder="如 config.publish" />
          </label>
          <label>
            对象类型
            <input value={targetType} onChange={(e) => setTargetType(e.target.value)} placeholder="config / zone / ..." />
          </label>
          <label>
            对象定位
            <input value={targetRef} onChange={(e) => setTargetRef(e.target.value)} />
          </label>
          <label>
            起始时间
            <input type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
          </label>
          <label>
            截止时间
            <input type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
          </label>
          <div className="form-actions">
            <button type="submit">查询</button>
          </div>
        </form>
      </section>

      <section className="panel">
        {isError && <p className="error-text">加载失败：{(error as Error).message}</p>}
        {isLoading ? (
          <p>加载中…</p>
        ) : (
          <>
            <table className="data-table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>环境</th>
                  <th>操作人</th>
                  <th>动作</th>
                  <th>对象类型</th>
                  <th>对象定位</th>
                  <th>结果</th>
                  <th>来源 IP</th>
                  <th>详情</th>
                </tr>
              </thead>
              <tbody>
                {data && data.items.length > 0 ? (
                  data.items.map((a, idx) => (
                    <tr key={`${a.createdAt}-${idx}`}>
                      <td>{formatTime(a.createdAt)}</td>
                      <td>{a.namespace || '-'}</td>
                      <td>{a.operator}</td>
                      <td>{a.action}</td>
                      <td>{a.targetType}</td>
                      <td className="mono">{a.targetRef}</td>
                      <td>{a.result === 'fail' ? <span className="badge badge-lost">fail</span> : 'ok'}</td>
                      <td>{a.clientIp || '-'}</td>
                      <td className="mono detail-cell">{a.detail || '-'}</td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td colSpan={9} className="empty">
                      无审计记录
                    </td>
                  </tr>
                )}
              </tbody>
            </table>

            <div className="pager">
              <button type="button" disabled={page <= 1 || isFetching} onClick={() => goPage(page - 1)}>
                上一页
              </button>
              <span>
                第 {page} / {totalPages} 页（共 {total} 条）
              </span>
              <button type="button" disabled={page >= totalPages || isFetching} onClick={() => goPage(page + 1)}>
                下一页
              </button>
            </div>
          </>
        )}
      </section>
    </div>
  )
}
