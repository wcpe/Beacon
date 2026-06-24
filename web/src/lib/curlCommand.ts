// admin API token「复制为 curl」命令构造（FR-90，见 ADR-0042）。
//
// 把一把刚签发 / 重置的 admin API token（FR-42 的 bk_ 密钥）拼成一条可直接粘贴运行的
// curl 示例命令：带认证头 X-Beacon-Api-Key、指向一只读 admin 端点（/system/status）。
// 纯函数、无副作用、可穷举单测；token 仅在浏览器内存于展示期拼接，不落任何持久化 / 日志。
//
// 样例固定指向只读端点作通用模板（不逐端点全量生成，守范围纪律）；运维据此改 URL / 方法即可。

// API 基址前缀（与 web/src/api/client.ts 的 BASE 对齐）
const API_PREFIX = '/admin/v1'

// 样例只读端点：任何角色（含 readonly）均可调，作 curl 上手模板
const SAMPLE_PATH = '/system/status'

// 认证头名（与 internal/server/middleware.go 的 apiKeyHeader 对齐）
const API_KEY_HEADER = 'X-Beacon-Api-Key'

// shellQuote 用单引号包裹并安全转义内部单引号（POSIX shell：闭合引号→插入转义单引号→重开引号）。
// 形如 a'b → 'a'\''b'，确保任意 token 字符都不破坏命令结构、不被 shell 误解析。
function shellQuote(s: string): string {
  return `'${s.replace(/'/g, "'\\''")}'`
}

// buildApiKeyCurl 的可选项：base 为 admin API 基址（含 /admin/v1）。
export interface CurlOptions {
  // admin API 基址（如 https://host/admin/v1）；缺省回退相对前缀 /admin/v1
  base?: string
}

// buildApiKeyCurl 构造带认证头、指向只读样例端点的 curl 命令字符串。
export function buildApiKeyCurl(key: string, opts: CurlOptions = {}): string {
  // base 归一化：去掉末尾斜杠，避免与样例路径拼出双斜杠
  const base = (opts.base ?? API_PREFIX).replace(/\/+$/, '')
  const url = base + SAMPLE_PATH
  return `curl -H ${shellQuote(`${API_KEY_HEADER}: ${key}`)} ${shellQuote(url)}`
}

// apiBaseFromLocation 由当前站点推导 admin API 基址（origin + /admin/v1），供 UI 拼真实可达的命令。
export function apiBaseFromLocation(): string {
  return `${window.location.origin}${API_PREFIX}`
}
