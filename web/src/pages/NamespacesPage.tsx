// 环境管理页：列出环境（namespace）+ 新建。

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createNamespace, listNamespaces } from '../api/client'
import MessageBar from '../components/MessageBar'
import { useMessage } from '../components/useMessage'

export default function NamespacesPage() {
  const qc = useQueryClient()
  const msg = useMessage()
  const [code, setCode] = useState('')
  const [name, setName] = useState('')

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['namespaces'],
    queryFn: listNamespaces,
  })

  const createMut = useMutation({
    mutationFn: () => createNamespace(code.trim(), name.trim()),
    onSuccess: (ns) => {
      msg.showSuccess(`已新建环境 ${ns.code}`)
      setCode('')
      setName('')
      qc.invalidateQueries({ queryKey: ['namespaces'] })
    },
    onError: (e: Error) => msg.showError(e.message),
  })

  function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!code.trim() || !name.trim()) {
      msg.showError('环境编码与名称均为必填')
      return
    }
    createMut.mutate()
  }

  return (
    <div className="page">
      <h1>环境管理</h1>
      <MessageBar message={msg.message} onClose={msg.clear} />

      <section className="panel">
        <h2>新建环境</h2>
        <form className="form-inline" onSubmit={onCreate}>
          <label>
            编码
            <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="如 prod" />
          </label>
          <label>
            名称
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="如 生产环境" />
          </label>
          <button type="submit" disabled={createMut.isPending}>
            新建
          </button>
        </form>
      </section>

      <section className="panel">
        <h2>环境列表</h2>
        {isLoading && <p>加载中…</p>}
        {isError && <p className="error-text">加载失败：{(error as Error).message}</p>}
        {!isLoading && !isError && (
          <table className="data-table">
            <thead>
              <tr>
                <th>编码</th>
                <th>名称</th>
              </tr>
            </thead>
            <tbody>
              {data && data.length > 0 ? (
                data.map((ns) => (
                  <tr key={ns.code}>
                    <td>{ns.code}</td>
                    <td>{ns.name}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={2} className="empty">
                    暂无环境
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
