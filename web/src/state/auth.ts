// 全局「登录态」：管理台所有 /admin/v1 请求都需登录令牌（FR-11 鉴权，见 ADR-0009）。
// 令牌与操作者身份存 sessionStorage（关页即失效，内部运维最小实现）；
// 操作者身份由登录派生、写操作 operator 以认证身份为准（后端忽略手填），不再让前端录入。

import { useSyncExternalStore } from 'react'

// sessionStorage 键名
const TOKEN_KEY = 'beacon.token'
const OPERATOR_KEY = 'beacon.operator'

// 订阅者集合：登录态变化时通知所有使用方重渲染
const listeners = new Set<() => void>()

// 当前登录态快照（避免 useSyncExternalStore 每次返回新对象引发无限重渲染）
let snapshot: AuthState = readFromStorage()

// 登录态：令牌 + 操作者；未登录时 token 为空串
export interface AuthState {
  token: string
  operator: string
}

// 从 sessionStorage 读取登录态（不可用时回退空态）
function readFromStorage(): AuthState {
  try {
    return {
      token: sessionStorage.getItem(TOKEN_KEY) ?? '',
      operator: sessionStorage.getItem(OPERATOR_KEY) ?? '',
    }
  } catch {
    return { token: '', operator: '' }
  }
}

// 写入 sessionStorage 并刷新快照、广播变化
function persist(state: AuthState): void {
  try {
    sessionStorage.setItem(TOKEN_KEY, state.token)
    sessionStorage.setItem(OPERATOR_KEY, state.operator)
  } catch {
    // 隐私模式等场景写入失败，忽略（仅影响持久化，不影响本次会话使用）
  }
  snapshot = state
  for (const l of listeners) l()
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => {
    listeners.delete(cb)
  }
}

function getSnapshot(): AuthState {
  return snapshot
}

// 登录成功：保存令牌与操作者身份
export function setAuth(token: string, operator: string): void {
  persist({ token, operator })
}

// 登出 / 令牌失效：清空登录态
export function clearAuth(): void {
  persist({ token: '', operator: '' })
}

// 取当前令牌（供 API 客户端注入 Authorization 头；非 React 上下文可直接调用）
export function currentToken(): string {
  return snapshot.token
}

// useAuth 返回当前登录态，登录态变化时组件重渲染。
export function useAuth(): AuthState {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
