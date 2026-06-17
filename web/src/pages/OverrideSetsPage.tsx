// 三方文件覆盖兼容页（override-set，FR-15）：按 namespace/group 过滤列表。
// 选中进入详情：元数据 + 发布前 dry-run 只读预览（将覆盖哪些文件/执行什么命令 + 勾选确认门控发布）+ 历史/回滚。
// 命令执行依赖鉴权且 agent 本地白名单放行，前端仅展示与确认，不做灰度向导（FR-9/P2 红线外）。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from 'react-router-dom'
import {
  dryRunOverrideSet,
  getOverrideSet,
  listOverrideSetRevisions,
  listOverrideSets,
  publishOverrideSet,
  rollbackOverrideSet,
} from '../api/client'
import type { OverrideSetFilter } from '../api/client'
import { formatTime } from '../api/format'
import { useOperator } from '../state/operator'
import MessageBar from '../components/MessageBar'
import { useMessage } from '../components/useMessage'

export default function OverrideSetsPage() {
  const navigate = useNavigate()
  const { id } = useParams<{ id: string }>()
  const selectedId = id ? Number(id) : null
  const msg = useMessage()

  const [fNamespace, setFNamespace] = useState('')
  const [fGroup, setFGroup] = useState('')
  const [filter, setFilter] = useState<OverrideSetFilter>({})

  const list = useQuery({
    queryKey: ['override-sets', filter],
    queryFn: () => listOverrideSets(filter),
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({ namespace: fNamespace.trim() || undefined, group: fGroup.trim() || undefined })
  }

  return (
    <div className="page">
      <h1>三方文件覆盖集</h1>
      <MessageBar message={msg.message} onClose={msg.clear} />

      <section className="panel">
        <h2>覆盖集列表</h2>
        <form className="form-grid" onSubmit={onSearch}>
          <label>
            环境
            <input value={fNamespace} onChange={(e) => setFNamespace(e.target.value)} />
          </label>
          <label>
            大区
            <input value={fGroup} onChange={(e) => setFGroup(e.target.value)} />
          </label>
          <div className="form-actions">
            <button type="submit">查询</button>
          </div>
        </form>

        {list.isError && <p className="error-text">加载失败：{(list.error as Error).message}</p>}
        {list.isLoading ? (
          <p>加载中…</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>ID</th>
                <th>名称</th>
                <th>环境</th>
                <th>大区</th>
                <th>覆盖层</th>
                <th>目标目录</th>
                <th>重载命令</th>
                <th>版本</th>
                <th>状态</th>
                <th>更新时间</th>
              </tr>
            </thead>
            <tbody>
              {list.data && list.data.length > 0 ? (
                list.data.map((o) => (
                  <tr
                    key={o.id}
                    className={selectedId === o.id ? 'row-selected clickable' : 'clickable'}
                    onClick={() => navigate(`/override-sets/${o.id}`)}
                  >
                    <td>{o.id}</td>
                    <td>{o.name}</td>
                    <td>{o.namespace}</td>
                    <td>{o.group}</td>
                    <td>{o.scopeLevel}</td>
                    <td className="mono">{o.targetRoot}</td>
                    <td className="mono">{o.reloadCommand || '-'}</td>
                    <td>{o.version}</td>
                    <td>{o.enabled ? '启用' : '已删'}</td>
                    <td>{formatTime(o.updatedAt)}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={10} className="empty">
                    无覆盖集
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        )}
      </section>

      {selectedId !== null && (
        <OverrideSetDetail key={selectedId} id={selectedId} onClosed={() => navigate('/override-sets')} />
      )}
    </div>
  )
}

// 覆盖集详情：元数据 + dry-run 只读预览（高危覆盖安全闸）+ 勾选确认门控发布 + 历史/回滚。
function OverrideSetDetail({ id, onClosed }: { id: number; onClosed: () => void }) {
  const qc = useQueryClient()
  const msg = useMessage()
  const [operator] = useOperator()

  // 发布表单（确认勾选后才允许提交）
  const [targetRoot, setTargetRoot] = useState('')
  const [reloadCommand, setReloadCommand] = useState('')
  const [comment, setComment] = useState('')
  const [confirmed, setConfirmed] = useState(false)

  const detail = useQuery({ queryKey: ['override-set', id], queryFn: () => getOverrideSet(id) })
  const revisions = useQuery({
    queryKey: ['override-set-revisions', id],
    queryFn: () => listOverrideSetRevisions(id),
  })
  const dryRun = useQuery({ queryKey: ['override-set-dryrun', id], queryFn: () => dryRunOverrideSet(id) })

  function invalidateAll() {
    qc.invalidateQueries({ queryKey: ['override-set', id] })
    qc.invalidateQueries({ queryKey: ['override-set-revisions', id] })
    qc.invalidateQueries({ queryKey: ['override-set-dryrun', id] })
    qc.invalidateQueries({ queryKey: ['override-sets'] })
  }

  const publishMut = useMutation({
    mutationFn: () => publishOverrideSet(id, targetRoot.trim(), reloadCommand.trim(), comment.trim()),
    onSuccess: (r) => {
      msg.showSuccess(`已发布覆盖集版本 ${r.version}（目标 ${r.targetRoot}）`)
      setComment('')
      setConfirmed(false)
      invalidateAll()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const rollbackMut = useMutation({
    mutationFn: (toVersion: number) => rollbackOverrideSet(id, toVersion, `回滚到版本 ${toVersion}`),
    onSuccess: (r) => {
      msg.showSuccess(`已回滚，新版本 ${r.version}`)
      invalidateAll()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  // 把当前覆盖集的目标目录/重载命令载入发布表单，便于在此基础上改。
  function fillFromCurrent() {
    if (detail.data) {
      setTargetRoot(detail.data.targetRoot)
      setReloadCommand(detail.data.reloadCommand)
    }
  }

  function onPublish(e: React.FormEvent) {
    e.preventDefault()
    if (!msg.requireOperator(operator)) return
    if (!targetRoot.trim()) {
      msg.showError('目标目录不能为空')
      return
    }
    if (!confirmed) {
      msg.showError('请先勾选确认 dry-run 预览的覆盖文件与重载命令')
      return
    }
    publishMut.mutate()
  }

  function onRollback(version: number) {
    if (!msg.requireOperator(operator)) return
    if (!window.confirm(`确认回滚覆盖集到版本 ${version}？将作为新版本发布。`)) return
    rollbackMut.mutate(version)
  }

  return (
    <section className="panel detail">
      <div className="detail-head">
        <h2>覆盖集详情 #{id}</h2>
        <button type="button" onClick={onClosed}>
          关闭
        </button>
      </div>
      <MessageBar message={msg.message} onClose={msg.clear} />

      {detail.isError && <p className="error-text">加载失败：{(detail.error as Error).message}</p>}
      {detail.data && (
        <dl className="meta-grid">
          <dt>名称</dt>
          <dd>{detail.data.name}</dd>
          <dt>环境</dt>
          <dd>{detail.data.namespace}</dd>
          <dt>大区</dt>
          <dd>{detail.data.group}</dd>
          <dt>覆盖层</dt>
          <dd>
            {detail.data.scopeLevel}
            {detail.data.scopeTarget ? ` / ${detail.data.scopeTarget}` : ''}
          </dd>
          <dt>目标目录</dt>
          <dd className="mono">{detail.data.targetRoot}</dd>
          <dt>重载命令</dt>
          <dd className="mono">{detail.data.reloadCommand || '（无）'}</dd>
          <dt>当前版本</dt>
          <dd>{detail.data.version}</dd>
          <dt>状态</dt>
          <dd>{detail.data.enabled ? '启用' : '已删'}</dd>
        </dl>
      )}

      <h3>发布前 dry-run 预览（高危覆盖安全闸）</h3>
      {dryRun.isError && <p className="error-text">预览失败：{(dryRun.error as Error).message}</p>}
      {dryRun.isFetching && <p>预览中…</p>}
      {dryRun.data && (
        <div className="dry-run-preview">
          <p>
            将向目标目录 <span className="mono">{dryRun.data.targetRoot}</span> 覆盖以下{' '}
            {dryRun.data.memberPaths.length} 个文件：
          </p>
          <ul className="mono">
            {dryRun.data.memberPaths.length > 0 ? (
              dryRun.data.memberPaths.map((p) => <li key={p}>{p}</li>)
            ) : (
              <li>（无成员文件）</li>
            )}
          </ul>
          <p>
            覆盖后执行重载命令：<span className="mono">{dryRun.data.reloadCommand || '（无）'}</span>
            （首 token <span className="mono">{dryRun.data.commandFirstToken || '-'}</span>，须在 agent 本地白名单内才会执行）
          </p>
        </div>
      )}

      <h3>发布新版本</h3>
      <form onSubmit={onPublish} className="publish-form">
        <button type="button" className="link-btn" onClick={fillFromCurrent}>
          载入当前目标/命令到表单
        </button>
        <label>
          目标插件目录（plugins/&lt;plugin&gt; 内）
          <input value={targetRoot} onChange={(e) => setTargetRoot(e.target.value)} placeholder="如 plugins/DeluxeMenus" />
        </label>
        <label>
          重载命令（单条控制台命令，须在 agent 本地白名单内）
          <input value={reloadCommand} onChange={(e) => setReloadCommand(e.target.value)} placeholder="如 deluxemenus reload" />
        </label>
        <input
          className="comment-input"
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          placeholder="变更备注（可选）"
        />
        <label className="confirm-check">
          <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} />
          我已确认上方 dry-run 预览的覆盖文件与重载命令
        </label>
        <div className="form-actions">
          <button type="submit" disabled={publishMut.isPending || !confirmed}>
            发布
          </button>
        </div>
      </form>

      <h3>历史版本</h3>
      {revisions.isLoading ? (
        <p>加载中…</p>
      ) : (
        <table className="data-table">
          <thead>
            <tr>
              <th>版本</th>
              <th>目标目录</th>
              <th>重载命令</th>
              <th>操作人</th>
              <th>备注</th>
              <th>创建时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {revisions.data && revisions.data.length > 0 ? (
              revisions.data.map((rev) => (
                <tr key={rev.version}>
                  <td>{rev.version}</td>
                  <td className="mono">{rev.targetRoot}</td>
                  <td className="mono">{rev.reloadCommand || '-'}</td>
                  <td>{rev.operator}</td>
                  <td>{rev.comment || '-'}</td>
                  <td>{formatTime(rev.createdAt)}</td>
                  <td>
                    <button type="button" disabled={rollbackMut.isPending} onClick={() => onRollback(rev.version)}>
                      回滚到此
                    </button>
                  </td>
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={7} className="empty">
                  无历史版本
                </td>
              </tr>
            )}
          </tbody>
        </table>
      )}
    </section>
  )
}
