// 鉴权域 API（FR-179）：管理台登录 / 登出，消费控制面 /admin/v1/auth/*（见 docs/API.md）。
// 复用集群域的全站统一请求封装（含 Authorization 注入与 401 处理）。
// 登录端点自身无需令牌（此时本地也无令牌）；登出需令牌（后端取认证身份记审计），返回 204。
import { request } from './cluster'

/** 登录响应：无状态 HMAC 令牌 + 操作者身份（后者仅供前端页眉展示）。 */
export interface LoginResult {
  token: string
  operator: string
}

/** 登录：用户名 + 口令 → 令牌；凭据错后端回 401（BAD_CREDENTIALS），message 已脱敏可直接展示。 */
export function login(username: string, password: string): Promise<LoginResult> {
  return request('POST', '/admin/v1/auth/login', { username, password })
}

/** 登出：后端仅记一条审计（令牌无状态、服务端无会话可吊销），返回 204。 */
export function logout(): Promise<void> {
  return request('POST', '/admin/v1/auth/logout')
}
