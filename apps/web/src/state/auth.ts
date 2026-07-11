// 全局「登录态」（FR-179）：管理台所有 /admin/* 请求都需登录令牌（对齐 Legacy web/src/state/auth.ts）。
// 令牌与操作者身份存 localStorage 持久（关页不失效，刷新自动恢复登录态）；
// 操作者身份由登录响应派生，仅用于页眉展示，写操作 operator 后端以认证身份为准。
//
// 同时承载「令牌失效（401）全局回调」注册：请求层遇 401 时清令牌并通知，由应用层
// 注册跳登录（避免 api 反向依赖 router）。放这里让所有登录态相关逻辑单一内聚。

import { useSyncExternalStore } from 'react'

// localStorage 键名
const TOKEN_KEY = 'beacon.token'
const OPERATOR_KEY = 'beacon.operator'

// 订阅者集合：登录态变化时通知所有使用方重渲染
const listeners = new Set<() => void>()

// 当前登录态快照（避免 useSyncExternalStore 每次返回新对象引发无限重渲染）
let snapshot: AuthState = readFromStorage()

// 令牌失效（401）全局回调；由应用层注册（如跳登录页），避免请求层反向依赖 router。
let unauthorizedHandler: (() => void) | null = null

// 登录态：令牌 + 操作者；未登录时 token 为空串
export interface AuthState {
  token: string
  operator: string
}

// 从 localStorage 读取登录态（不可用时回退空态）
function readFromStorage(): AuthState {
  try {
    return {
      token: localStorage.getItem(TOKEN_KEY) ?? '',
      operator: localStorage.getItem(OPERATOR_KEY) ?? '',
    }
  } catch {
    return { token: '', operator: '' }
  }
}

// 写入 localStorage 并刷新快照、广播变化
function persist(state: AuthState): void {
  try {
    localStorage.setItem(TOKEN_KEY, state.token)
    localStorage.setItem(OPERATOR_KEY, state.operator)
  } catch {
    // 隐私模式等场景写入失败，忽略（仅影响持久化，不影响本次会话使用）
  }
  snapshot = state
  for (const listener of listeners) {
    listener()
  }
}

function subscribe(callback: () => void): () => void {
  listeners.add(callback)
  return () => {
    listeners.delete(callback)
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

// 取当前令牌（供请求层注入 Authorization 头；非 React 上下文可直接调用）
export function currentToken(): string {
  return snapshot.token
}

// 是否已登录（供路由守卫判定）
export function isAuthenticated(): boolean {
  return snapshot.token !== ''
}

// 注册 401 处理器：任意 /admin/* 请求遇 401 时触发（请求层已清登录态，处理器负责跳登录）。
export function setOnUnauthorized(handler: () => void): void {
  unauthorizedHandler = handler
}

// 通知令牌失效（请求层遇 401 时调用）：有注册处理器则触发。
export function notifyUnauthorized(): void {
  unauthorizedHandler?.()
}

// useAuth 返回当前登录态，登录态变化时组件重渲染。
export function useAuth(): AuthState {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
}
