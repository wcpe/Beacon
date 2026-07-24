// 真后端 E2E 二进制构建：构建前端 apps/web/dist（被控制面 go:embed 内嵌）+ go build 出 beacon-e2e 二进制。
// 落 dist/beacon-e2e[.exe]，由 playwright.config.ts 的 real 项目 webServer 以 sqlite + 固定凭据启动。
// 需本机有 go + C 编译器（CGO，sqlite 驱动 mattn/go-sqlite3 需要），见 docs/OPERATIONS.md §7。

import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { mkdirSync } from 'node:fs'

const repoRoot = fileURLToPath(new URL('../../../', import.meta.url))
const isWin = process.platform === 'win32'
const binName = isWin ? 'beacon-e2e.exe' : 'beacon-e2e'
const binPath = fileURLToPath(new URL(`../../../dist/${binName}`, import.meta.url))
// 真后端运行目录（sqlite 库 / 首启 config.yml 落此，不污染仓库根）
const runDir = fileURLToPath(new URL('../../.e2e-real-run', import.meta.url))

function commandSpec(cmd, args) {
  if (isWin && cmd === 'pnpm') {
    return { cmd: 'cmd.exe', args: ['/d', '/s', '/c', cmd, ...args] }
  }
  return { cmd, args }
}

function run(cmd, args, cwd) {
  console.log(`\n[真后端构建] ${cmd} ${args.join(' ')}  (cwd=${cwd})`)
  const spec = commandSpec(cmd, args)
  const r = spawnSync(spec.cmd, spec.args, { cwd, stdio: 'inherit' })
  if (r.status !== 0) {
    throw new Error(`命令失败（退出码 ${r.status}）：${cmd} ${args.join(' ')}`)
  }
}

// ① 构建第二版前端产物 apps/web/dist（控制面 go:embed 需要）
run('pnpm', ['install', '--frozen-lockfile'], repoRoot)
run('pnpm', ['--filter', '@beacon/web', 'build'], repoRoot)

// ② 准备运行目录
mkdirSync(runDir, { recursive: true })

// ③ go build 出 beacon-e2e（CGO 默认开，内嵌已构建的 apps/web/dist）
run('go', ['build', '-o', binPath, './apps/server/cmd/beacon'], repoRoot)

console.log(`\n[真后端构建] 完成：${binPath}`)
