import { useQuery } from '@tanstack/react-query'
import { listNamespaces } from './api/client'

// 空壳管理台页面：加载时拉取 namespace 列表以验证 apiClient 链路打通。
export default function App() {
  const { data, isLoading, isError } = useQuery({
    queryKey: ['namespaces'],
    queryFn: listNamespaces,
  })

  return (
    <main>
      <h1>Beacon 管理台</h1>
      <section>
        <h2>环境列表</h2>
        {isLoading && <p>加载中…</p>}
        {isError && <p>加载环境列表失败，请确认控制面已启动后重试。</p>}
        {!isLoading && !isError && (
          data && data.length > 0 ? (
            <ul>
              {data.map((ns) => (
                <li key={ns}>{ns}</li>
              ))}
            </ul>
          ) : (
            <p>暂无环境。</p>
          )
        )}
      </section>
    </main>
  )
}
