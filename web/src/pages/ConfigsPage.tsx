// 配置中心页：按 namespace/group/dataId/scopeLevel 过滤列表 + 新建配置。
// 选中某行进入详情（发布/历史/diff/回滚/软删），详情走子路由 /configs/:id 可深链。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams } from 'react-router-dom'
import { createConfig, listConfigs } from '../api/client'
import type { ConfigFilter, CreateConfigParams } from '../api/client'
import { formatTime } from '../api/format'
import MessageBar from '../components/MessageBar'
import { useMessage } from '../components/useMessage'
import ConfigDetail from './ConfigDetail'

// 新建表单初值
const EMPTY_FORM = {
  namespace: '',
  group: '',
  dataId: '',
  scopeLevel: 'global',
  scopeTarget: '',
  format: 'yaml',
  content: '',
  comment: '',
}

export default function ConfigsPage() {
  const qc = useQueryClient()
  const msg = useMessage()
  const navigate = useNavigate()
  const { id } = useParams<{ id: string }>()
  const selectedId = id ? Number(id) : null

  // 过滤草稿与生效值
  const [fNamespace, setFNamespace] = useState('')
  const [fGroup, setFGroup] = useState('')
  const [fDataId, setFDataId] = useState('')
  const [fScopeLevel, setFScopeLevel] = useState('')
  const [filter, setFilter] = useState<ConfigFilter>({})

  // 新建表单
  const [form, setForm] = useState(EMPTY_FORM)
  const [showCreate, setShowCreate] = useState(false)

  const list = useQuery({
    queryKey: ['configs', filter],
    queryFn: () => listConfigs(filter),
  })

  const createMut = useMutation({
    mutationFn: (params: CreateConfigParams) => createConfig(params),
    onSuccess: (c) => {
      msg.showSuccess(`已新建配置 #${c.id}（${c.dataId}）`)
      setForm(EMPTY_FORM)
      setShowCreate(false)
      qc.invalidateQueries({ queryKey: ['configs'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onSearch(e: React.FormEvent) {
    e.preventDefault()
    setFilter({
      namespace: fNamespace.trim() || undefined,
      group: fGroup.trim() || undefined,
      dataId: fDataId.trim() || undefined,
      scopeLevel: fScopeLevel.trim() || undefined,
    })
  }

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!form.namespace.trim() || !form.group.trim() || !form.dataId.trim()) {
      msg.showError('环境、大区、dataId 均为必填')
      return
    }
    createMut.mutate({
      namespace: form.namespace.trim(),
      group: form.group.trim(),
      dataId: form.dataId.trim(),
      scopeLevel: form.scopeLevel,
      scopeTarget: form.scopeTarget.trim(),
      format: form.format,
      content: form.content,
      comment: form.comment.trim(),
    })
  }

  return (
    <div className="page">
      <h1>配置中心</h1>
      <MessageBar message={msg.message} onClose={msg.clear} />

      <section className="panel">
        <div className="panel-head">
          <h2>配置列表</h2>
          <button type="button" onClick={() => setShowCreate((v) => !v)}>
            {showCreate ? '收起新建' : '新建配置'}
          </button>
        </div>

        {showCreate && (
          <form className="form-grid create-form" onSubmit={onCreate}>
            <label>
              环境
              <input value={form.namespace} onChange={(e) => setForm({ ...form, namespace: e.target.value })} />
            </label>
            <label>
              大区
              <input
                value={form.group}
                onChange={(e) => setForm({ ...form, group: e.target.value })}
                placeholder="global 层占位 __GLOBAL__"
              />
            </label>
            <label>
              dataId
              <input value={form.dataId} onChange={(e) => setForm({ ...form, dataId: e.target.value })} />
            </label>
            <label>
              覆盖层
              <select value={form.scopeLevel} onChange={(e) => setForm({ ...form, scopeLevel: e.target.value })}>
                <option value="global">global</option>
                <option value="group">group</option>
                <option value="zone">zone</option>
                <option value="server">server</option>
              </select>
            </label>
            <label>
              覆盖目标
              <input
                value={form.scopeTarget}
                onChange={(e) => setForm({ ...form, scopeTarget: e.target.value })}
                placeholder="zone/server 等层的目标键"
              />
            </label>
            <label>
              格式
              <select value={form.format} onChange={(e) => setForm({ ...form, format: e.target.value })}>
                <option value="yaml">yaml</option>
                <option value="properties">properties</option>
                <option value="json">json</option>
              </select>
            </label>
            <label className="full">
              内容
              <textarea
                rows={8}
                className="content-editor"
                value={form.content}
                onChange={(e) => setForm({ ...form, content: e.target.value })}
              />
            </label>
            <label className="full">
              备注
              <input value={form.comment} onChange={(e) => setForm({ ...form, comment: e.target.value })} />
            </label>
            <div className="form-actions">
              <button type="submit" disabled={createMut.isPending}>
                创建并首次发布
              </button>
            </div>
          </form>
        )}

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
            dataId
            <input value={fDataId} onChange={(e) => setFDataId(e.target.value)} />
          </label>
          <label>
            覆盖层
            <select value={fScopeLevel} onChange={(e) => setFScopeLevel(e.target.value)}>
              <option value="">全部</option>
              <option value="global">global</option>
              <option value="group">group</option>
              <option value="zone">zone</option>
              <option value="server">server</option>
            </select>
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
                <th>环境</th>
                <th>大区</th>
                <th>dataId</th>
                <th>覆盖层</th>
                <th>目标</th>
                <th>格式</th>
                <th>版本</th>
                <th>md5</th>
                <th>状态</th>
                <th>更新时间</th>
              </tr>
            </thead>
            <tbody>
              {list.data && list.data.length > 0 ? (
                list.data.map((c) => (
                  <tr
                    key={c.id}
                    className={selectedId === c.id ? 'row-selected clickable' : 'clickable'}
                    onClick={() => navigate(`/configs/${c.id}`)}
                  >
                    <td>{c.id}</td>
                    <td>{c.namespace}</td>
                    <td>{c.group}</td>
                    <td className="mono">{c.dataId}</td>
                    <td>{c.scopeLevel}</td>
                    <td>{c.scopeTarget || '-'}</td>
                    <td>{c.format}</td>
                    <td>{c.version}</td>
                    <td className="mono">{c.md5.slice(0, 8)}</td>
                    <td>{c.enabled ? '启用' : '已删'}</td>
                    <td>{formatTime(c.updatedAt)}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={11} className="empty">
                    无配置项
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        )}
      </section>

      {selectedId !== null && (
        <ConfigDetail key={selectedId} id={selectedId} onClosed={() => navigate('/configs')} />
      )}
    </div>
  )
}
