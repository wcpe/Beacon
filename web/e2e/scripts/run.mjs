// 跨平台 E2E 启动器：按 target（mock / real）设 PW_TARGET 并以对应 project 跑 playwright。
// 用法：node e2e/scripts/run.mjs <mock|real> [...透传给 playwright 的额外参数]
// 目的：避免在 package.json 脚本里写平台相关的 env 赋值（Windows cmd 与 POSIX sh 语法不同），
//       同时确保只启动所需的 webServer（mock 不白等真后端构建，real 不起 vite）。

import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const target = process.argv[2]
if (target !== 'mock' && target !== 'real') {
  console.error('用法：node e2e/scripts/run.mjs <mock|real> [extra playwright args]')
  process.exit(2)
}
const extra = process.argv.slice(3)
const isWin = process.platform === 'win32'

// web/ 工程根（本脚本在 web/e2e/scripts 下，上溯三级到 web/）
const webRoot = fileURLToPath(new URL('../../', import.meta.url))

function commandSpec(cmd, args) {
  if (isWin && cmd === 'pnpm') {
    return { cmd: 'cmd.exe', args: ['/d', '/s', '/c', cmd, ...args] }
  }
  return { cmd, args }
}

// 真后端：先构建 apps/web 与二进制，再交给 playwright（其 webServer 起二进制）。
async function ensureRealBinary() {
  await runStep(process.execPath, [fileURLToPath(new URL('./build-real.mjs', import.meta.url))])
}

function runStep(cmd, args) {
  return new Promise((resolve, reject) => {
    const spec = commandSpec(cmd, args)
    const child = spawn(spec.cmd, spec.args, {
      cwd: webRoot,
      stdio: 'inherit',
    })
    child.on('exit', (code) =>
      code === 0 ? resolve() : reject(new Error(`${cmd} 退出码 ${code}`)),
    )
    child.on('error', reject)
  })
}

async function main() {
  if (target === 'real') {
    await ensureRealBinary()
  }
  // 透传 --project 与额外参数给 playwright；PW_TARGET 控制 webServer 选择（见 playwright.config.ts）。
  const args = ['playwright', 'test', '--project', target, ...extra]
  const spec = commandSpec('pnpm', ['exec', ...args])
  const child = spawn(spec.cmd, spec.args, {
    cwd: webRoot,
    stdio: 'inherit',
    env: { ...process.env, PW_TARGET: target },
  })
  child.on('exit', (code) => process.exit(code ?? 1))
  child.on('error', (e) => {
    console.error(e)
    process.exit(1)
  })
}

main().catch((e) => {
  console.error(e.message ?? e)
  process.exit(1)
})
