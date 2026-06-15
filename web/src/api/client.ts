// 管理台 API 轻量 fetch 封装。
// base 固定为 /admin/v1；开发期由 vite proxy 转发到本地控制面，生产期同源同端口。

// API 基址：所有管理台接口的公共前缀
const BASE = '/admin/v1'

// 统一错误体（与 docs/API.md 对齐）：失败时后端返回 { code, message, traceId? }
interface ApiError {
  code?: string
  message?: string
  traceId?: string
}

// 发起 GET 请求并解析 JSON；非 2xx 时抛出携带中文说明的错误。
async function getJson<T>(path: string): Promise<T> {
  const resp = await fetch(`${BASE}${path}`, {
    headers: { Accept: 'application/json' },
  })
  if (!resp.ok) {
    // 尝试解析统一错误体，取后端中文 message；解析失败则回退到状态码
    let detail = `HTTP ${resp.status}`
    try {
      const err = (await resp.json()) as ApiError
      if (err.message) detail = err.message
    } catch {
      // 响应体非 JSON，保留状态码作为提示
    }
    throw new Error(detail)
  }
  return (await resp.json()) as T
}

// 拉取环境（namespace）列表：GET /admin/v1/namespaces。
// 后端返回形态尚未完全锁定，这里兼容「裸数组」与「{ items|namespaces: [...] }」两种包装，
// 且数组元素允许是字符串或 { namespace } 对象，统一归一化为字符串数组。
export async function listNamespaces(): Promise<string[]> {
  const raw = await getJson<unknown>('/namespaces')
  const list = extractList(raw)
  return list.map(toNamespaceString).filter((s): s is string => s.length > 0)
}

// 从响应中取出列表：兼容裸数组与常见包装字段
function extractList(raw: unknown): unknown[] {
  if (Array.isArray(raw)) return raw
  if (raw && typeof raw === 'object') {
    const obj = raw as Record<string, unknown>
    if (Array.isArray(obj.items)) return obj.items
    if (Array.isArray(obj.namespaces)) return obj.namespaces
  }
  return []
}

// 把单个列表元素归一化为 namespace 字符串
function toNamespaceString(item: unknown): string {
  if (typeof item === 'string') return item
  if (item && typeof item === 'object') {
    const ns = (item as Record<string, unknown>).namespace
    if (typeof ns === 'string') return ns
  }
  return ''
}
