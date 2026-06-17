// 「操作人」状态：写操作（发布/回滚/改派/下线等）的 operator 字段来源。
// FR-11 鉴权落地后，操作者身份由登录令牌决定、后端以认证身份为准（忽略请求手填），
// 故 operator 从登录态派生（见 state/auth.ts）。本模块保留既有 useOperator 出参形态，
// 让既有页面零改动；setOperator 不再生效（身份由令牌决定）。

import { useAuth } from './auth'

// 空操作：兼容既有页面对 setOperator 的调用（身份由登录令牌决定，手填不再生效）
function noop(): void {}

// useOperator 返回当前操作人与设置函数；operator 来自登录身份，setOperator 为兼容旧调用的空操作。
export function useOperator(): [string, (value: string) => void] {
  const { operator } = useAuth()
  return [operator, noop]
}
