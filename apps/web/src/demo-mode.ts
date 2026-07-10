// 演示模式判定（FR-159）：唯一真源，供 main.tsx 与 Shell 共用。
//
// 判据：dev server（`pnpm dev`）恒为演示模式；生产构建仅当 `vite build --mode demo` 时开启。
// 常规 `vite build` 产出的是发布产物（由控制面 go:embed 内嵌），必须连接真实后端，
// 因此不注册 mock worker、也不产出 mockServiceWorker.js（见 vite.config.ts 的门控）。
export function isDemoMode(): boolean {
  return import.meta.env.DEV || import.meta.env.MODE === 'demo'
}
