// 交付大域共用的底层请求封装：收敛委托到集群域的全站统一封装（cluster.ts），复用其
// ApiClientError（保证 instanceof 判定一致），并统一鉴权注入与 401 处理（FR-179）。仅作再导出。
export { ApiClientError, buildQuery, request } from './cluster'
