// @beacon/contracts 入口（FR-155 前置）：第二版管理台对外响应契约类型的独立真源。
// 生产代码（apps/web api 层与页面）只依赖本包的纯类型，不再依赖 mock 包 @beacon/devmock；
// mock 包反向依赖本包，其 handler 的 `satisfies XxxResponse` 仍锚定这些契约以防 mock 与真契约漂移。
// 本包无任何运行时依赖，纯 type-only。

export * from './common'
export * from './cluster'
export * from './identity'
export * from './namespace'
export * from './metrics-health'
export * from './config-center'
export * from './file-assets'
export * from './system'
export * from './observability'
export * from './archive'
export * from './connections-messages'
export * from './delivery'
