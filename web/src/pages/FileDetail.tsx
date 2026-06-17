// 文件详情面板：展示当前整文件内容 + 元数据；编辑发布、历史版本、并排 diff、回滚、软删。
// 复刻 ConfigDetail 范式，套到文件对象（通道B，FR-14）。文件无 /diff 端点，
// diff 取两个历史版本内容并排展示（前端不算差异、不合并，仅并列原文）。

import { useState } from 'react'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  deleteFile,
  getFile,
  getFileRevision,
  listFileRevisions,
  publishFile,
  rollbackFile,
} from '../api/client'
import { formatTime } from '../api/format'
import { useOperator } from '../state/operator'
import CodeEditor from '../components/CodeEditor'
import MessageBar from '../components/MessageBar'
import { useMessage } from '../components/useMessage'

export default function FileDetail({ id, onClosed }: { id: number; onClosed: () => void }) {
  const qc = useQueryClient()
  const msg = useMessage()
  const [operator] = useOperator()

  // 发布表单
  const [content, setContent] = useState('')
  const [comment, setComment] = useState('')
  // diff 选择的两个版本
  const [diffFrom, setDiffFrom] = useState<number | ''>('')
  const [diffTo, setDiffTo] = useState<number | ''>('')

  const detail = useQuery({
    queryKey: ['file', id],
    queryFn: () => getFile(id),
  })

  const revisions = useQuery({
    queryKey: ['file-revisions', id],
    queryFn: () => listFileRevisions(id),
  })

  // 并排 diff：分别取两个版本的整文件内容（文件无 /diff 端点）
  const diffPair = useQueries({
    queries: [
      {
        queryKey: ['file-revision', id, diffFrom],
        queryFn: () => getFileRevision(id, Number(diffFrom)),
        enabled: diffFrom !== '',
      },
      {
        queryKey: ['file-revision', id, diffTo],
        queryFn: () => getFileRevision(id, Number(diffTo)),
        enabled: diffTo !== '',
      },
    ],
  })
  const [fromRev, toRev] = diffPair
  const diffReady = diffFrom !== '' && diffTo !== '' && fromRev.data && toRev.data
  const diffLoading = (diffFrom !== '' && fromRev.isFetching) || (diffTo !== '' && toRev.isFetching)
  const diffError = fromRev.error || toRev.error

  function invalidateAll() {
    qc.invalidateQueries({ queryKey: ['file', id] })
    qc.invalidateQueries({ queryKey: ['file-revisions', id] })
    qc.invalidateQueries({ queryKey: ['files'] })
  }

  const publishMut = useMutation({
    mutationFn: () => publishFile(id, content, operator, comment.trim()),
    onSuccess: (r) => {
      msg.showSuccess(`已发布版本 ${r.version}（md5 ${r.md5.slice(0, 8)}）`)
      setComment('')
      invalidateAll()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const rollbackMut = useMutation({
    mutationFn: (toVersion: number) => rollbackFile(id, toVersion, operator, `回滚到版本 ${toVersion}`),
    onSuccess: (r) => {
      msg.showSuccess(`已回滚，新版本 ${r.version}`)
      invalidateAll()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: () => deleteFile(id, operator, '管理台软删'),
    onSuccess: () => {
      msg.showSuccess('已软删该文件层')
      qc.invalidateQueries({ queryKey: ['files'] })
      onClosed()
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onPublish(e: React.FormEvent) {
    e.preventDefault()
    if (!content) {
      msg.showError('发布内容不能为空')
      return
    }
    publishMut.mutate()
  }

  function onRollback(version: number) {
    if (!window.confirm(`确认回滚到版本 ${version}？将作为新版本发布。`)) return
    rollbackMut.mutate(version)
  }

  function onDelete() {
    if (!window.confirm('确认软删该文件层？该层将从覆盖链脱落，下游 agent 据 manifest 删除镜像。')) return
    deleteMut.mutate()
  }

  // 进入详情后把当前内容填入发布框，便于在此基础上改
  function fillFromCurrent() {
    if (detail.data?.content !== undefined) setContent(detail.data.content)
  }

  return (
    <section className="panel detail">
      <div className="detail-head">
        <h2>文件详情 #{id}</h2>
        <button type="button" onClick={onClosed}>
          关闭
        </button>
      </div>
      <MessageBar message={msg.message} onClose={msg.clear} />

      {detail.isError && <p className="error-text">加载失败：{(detail.error as Error).message}</p>}
      {detail.isLoading && <p>加载中…</p>}
      {detail.data && (
        <>
          <dl className="meta-grid">
            <dt>环境</dt>
            <dd>{detail.data.namespace}</dd>
            <dt>大区</dt>
            <dd>{detail.data.group}</dd>
            <dt>路径</dt>
            <dd className="mono">{detail.data.path}</dd>
            <dt>覆盖层</dt>
            <dd>
              {detail.data.scopeLevel}
              {detail.data.scopeTarget ? ` / ${detail.data.scopeTarget}` : ''}
            </dd>
            <dt>当前版本</dt>
            <dd>{detail.data.version}</dd>
            <dt>md5</dt>
            <dd className="mono">{detail.data.md5}</dd>
            <dt>启用</dt>
            <dd>{detail.data.enabled ? '是' : '否（已软删）'}</dd>
            <dt>更新时间</dt>
            <dd>{formatTime(detail.data.updatedAt)}</dd>
          </dl>

          <h3>当前内容</h3>
          <pre className="content-view">{detail.data.content}</pre>

          <h3>发布新版本</h3>
          <form onSubmit={onPublish} className="publish-form">
            <button type="button" className="link-btn" onClick={fillFromCurrent}>
              载入当前内容到编辑框
            </button>
            <CodeEditor value={content} onChange={setContent} placeholder="在此编辑新版本整文件内容" />
            <input
              className="comment-input"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder="变更备注（可选）"
            />
            <div className="form-actions">
              <button type="submit" disabled={publishMut.isPending}>
                发布
              </button>
              <button type="button" className="btn-danger" disabled={deleteMut.isPending} onClick={onDelete}>
                软删此层
              </button>
            </div>
          </form>
        </>
      )}

      <h3>历史版本</h3>
      {revisions.isError && <p className="error-text">加载失败：{(revisions.error as Error).message}</p>}
      {revisions.isLoading ? (
        <p>加载中…</p>
      ) : (
        <table className="data-table">
          <thead>
            <tr>
              <th>版本</th>
              <th>md5</th>
              <th>操作人</th>
              <th>备注</th>
              <th>来源版本</th>
              <th>创建时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {revisions.data && revisions.data.length > 0 ? (
              revisions.data.map((rev) => (
                <tr key={rev.version}>
                  <td>{rev.version}</td>
                  <td className="mono">{rev.md5.slice(0, 8)}</td>
                  <td>{rev.operator}</td>
                  <td>{rev.comment || '-'}</td>
                  <td>{rev.sourceRevision ?? '-'}</td>
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

      <h3>版本对比（diff）</h3>
      <div className="diff-controls">
        <label>
          旧版本
          <select value={diffFrom} onChange={(e) => setDiffFrom(e.target.value ? Number(e.target.value) : '')}>
            <option value="">选择</option>
            {revisions.data?.map((rev) => (
              <option key={rev.version} value={rev.version}>
                v{rev.version}
              </option>
            ))}
          </select>
        </label>
        <label>
          新版本
          <select value={diffTo} onChange={(e) => setDiffTo(e.target.value ? Number(e.target.value) : '')}>
            <option value="">选择</option>
            {revisions.data?.map((rev) => (
              <option key={rev.version} value={rev.version}>
                v{rev.version}
              </option>
            ))}
          </select>
        </label>
      </div>
      {diffError && <p className="error-text">对比失败：{(diffError as Error).message}</p>}
      {diffLoading && <p>对比中…</p>}
      {diffReady && (
        <div className="diff-panes">
          <div className="diff-pane">
            <div className="diff-pane-title">v{fromRev.data!.version}</div>
            <pre className="content-view">{fromRev.data!.content}</pre>
          </div>
          <div className="diff-pane">
            <div className="diff-pane-title">v{toRev.data!.version}</div>
            <pre className="content-view">{toRev.data!.content}</pre>
          </div>
        </div>
      )}
    </section>
  )
}
