// 工作台原型数据 hook（FR-111 / FR-112）：直接 fetch mock 暴露的 /admin/v1/workbench/*。
// 原型不走 api/client（其契约锁定后端），故在此就近封装最小 fetch + react-query。
// 类型复用 mock/workbench 的形状（前后端原型同源，不另立契约）。

import { useQuery } from '@tanstack/react-query'
import { currentToken } from '@/state/auth'
import type {
  EffectiveFile,
  IngestScanItem,
  ManagedNode,
  OpLogEntry,
  PublishImpact,
  ScopeOption,
  ServerNode,
  ServerOption,
  SyncQueueRow,
  WorkbenchFile,
} from '@/api/mock/workbench'

const BASE = '/admin/v1/workbench'

// 最小 GET：带登录令牌、解析 JSON（原型用，错误直接抛）
async function get<T>(path: string): Promise<T> {
  const token = currentToken()
  const resp = await fetch(`${BASE}${path}`, {
    headers: { Accept: 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
  })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  return (await resp.json()) as T
}

// 受管配置树
export function useManagedTree() {
  return useQuery({
    queryKey: ['wb-managed-tree'],
    queryFn: () => get<{ items: ManagedNode[] }>('/managed-tree').then((r) => r.items),
  })
}

// 服务器实时 plugins 树
export function useServerTree() {
  return useQuery({
    queryKey: ['wb-server-tree'],
    queryFn: () => get<{ items: ServerNode[] }>('/server-tree').then((r) => r.items),
  })
}

// 同步队列
export function useSyncQueue() {
  return useQuery({
    queryKey: ['wb-sync-queue'],
    queryFn: () => get<{ items: SyncQueueRow[] }>('/sync-queue').then((r) => r.items),
  })
}

// 操作日志（撤回 / 回滚源）：历史大操作种子，运行期再叠加本地新增
export function useOperationLog() {
  return useQuery({
    queryKey: ['wb-operation-log'],
    queryFn: () => get<{ items: OpLogEntry[] }>('/operation-log').then((r) => r.items),
  })
}

// scope / server 候选
export function useWorkbenchOptions() {
  return useQuery({
    queryKey: ['wb-options'],
    queryFn: () => get<{ scopes: ScopeOption[]; servers: ServerOption[] }>('/options'),
  })
}

// 单个受管文件（编辑器用）；key 做 URL 编码
export function useWorkbenchFile(key: string | undefined) {
  return useQuery({
    queryKey: ['wb-file', key],
    queryFn: () => get<WorkbenchFile>(`/files/${encodeURIComponent(key!)}`),
    enabled: !!key,
  })
}

// 反向抓取扫描清单（待审核 ingest 浮层用）：全量扫描项 + 忽略规则
export function useIngestScanList() {
  return useQuery({
    queryKey: ['wb-ingest-scan'],
    queryFn: () => get<{ items: IngestScanItem[]; ignoreRules: string[] }>('/ingest-scan'),
  })
}

// 生效预览（受管面板「生效预览」视图）：某实例合并后有效树 + 逐键来源
export function useEffectivePreview(serverId: string | undefined) {
  return useQuery({
    queryKey: ['wb-effective', serverId],
    queryFn: () => get<{ items: EffectiveFile[] }>(`/effective/${encodeURIComponent(serverId!)}`).then((r) => r.items),
    enabled: !!serverId,
  })
}

// 发布影响面（改进 1）：按选中文件名解析受影响在线服清单 + 拓印差异台数。
// 依赖运行期选中集合（POST 传 names），用 react-query 按 names 缓存；发布面板打开时启用。
export function usePublishImpact(names: string[], enabled: boolean) {
  return useQuery({
    queryKey: ['wb-publish-impact', names],
    queryFn: async () => {
      const token = currentToken()
      const resp = await fetch(`${BASE}/publish-impact`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'application/json',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify({ names }),
      })
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
      return (await resp.json()) as PublishImpact
    },
    enabled: enabled && names.length > 0,
  })
}
