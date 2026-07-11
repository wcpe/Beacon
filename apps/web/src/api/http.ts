// 可观测 / 连接 / 指标域数据获取共用请求封装：收敛委托到集群域的全站统一封装（cluster.ts），
// 使鉴权注入（Authorization: Bearer）与 401 处理四处一致（FR-179）。仅作再导出，不重复实现。
export { ApiClientError, buildQuery, request } from './cluster'
