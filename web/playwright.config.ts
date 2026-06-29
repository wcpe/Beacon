// Playwright E2E 配置（WI-6）：两套 target，与 vitest 严格隔离。
//
// 设计要点：
//   - testDir = e2e/，与 vitest（src/**/*.test.*）互不串：vitest 只收 src 下用例，
//     playwright 只收 e2e/ 下 *.spec.ts，两者文件名后缀也不同（.test vs .spec）。
//   - 两个 project：
//       · mock —— 对「假后端（演示模式）」跑，由 webServer 起 `pnpm dev:mock`（vite mock 模式），
//         登录走登录页「演示模式」按钮，无需真后端。日常主跑、最稳，必须本机绿。
//       · real —— 对「真控制面」跑：globalSetup 先 `make web` + `go build` 出 beacon 单二进制（内嵌前端），
//         webServer 用 sqlite + 固定 admin 凭据 + 固定端口起它，登录走真凭据。覆盖关键链路。
//   - 两 project 各自独立 baseURL / webServer；用 grep 标签或 testMatch 区分用例归属。
//
// 运行：
//   pnpm test:e2e        仅 mock 项目（假后端）
//   pnpm test:e2e:real   仅 real 项目（真后端，需 go + gcc/CGO 构建 sqlite 版 beacon）

import { defineConfig, devices } from '@playwright/test'
import { fileURLToPath } from 'node:url'

// 假后端前端开发服务器端口（避开真后端 18848，避免两套并跑撞端口）
const MOCK_PORT = 5273
// 真后端控制面端口（sqlite 开发模式，固定端口，避开默认 8848 以免撞本机已起实例）
const REAL_PORT = 18848

// 真后端固定测试凭据（仅本地 E2E 用，非生产；经 env 注入控制面，见 e2e/real.setup.ts 与 scripts）。
export const REAL_ADMIN_USERNAME = 'admin'
export const REAL_ADMIN_PASSWORD = 'beacon-e2e-admin-pass'
export const REAL_AUTH_SECRET = 'beacon-e2e-auth-secret-0123456789'

// 仓库根（web/ 的上一级）：globalSetup 在此跑 make web + go build。
const repoRoot = fileURLToPath(new URL('..', import.meta.url))
// 构建出的真后端二进制路径（Windows 带 .exe）。
const beaconBin = fileURLToPath(
  new URL(
    process.platform === 'win32' ? '../dist/beacon-e2e.exe' : '../dist/beacon-e2e',
    import.meta.url,
  ),
)

export default defineConfig({
  // E2E 用例根目录，与 vitest 的 src/ 物理隔离
  testDir: './e2e',
  // 仅收 *.spec.ts，避免误收任何 *.test.*（与 vitest 后缀区分，双保险）
  testMatch: '**/*.spec.ts',
  // 单条用例超时（含真后端首启 + 页面渲染余量）
  timeout: 60_000,
  expect: { timeout: 10_000 },
  // 失败重试：CI 上重试 1 次抹平偶发抖动；本地不重试便于发现真问题
  retries: process.env.CI ? 1 : 0,
  // 串行 worker 数：本地默认并行；真后端单实例用例靠独立 project + 单 worker 串行（见 real 项目）
  fullyParallel: true,
  // 报告：列表（控制台）+ HTML（落 playwright-report/，已 gitignore）
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    // 失败时留痕便于排查
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    // 统一中文 locale，确保按中文文案选择器命中
    locale: 'zh-CN',
  },

  projects: [
    // ===== 假后端（演示模式）E2E =====
    {
      name: 'mock',
      testDir: './e2e/mock',
      use: {
        ...devices['Desktop Chrome'],
        baseURL: `http://localhost:${MOCK_PORT}`,
      },
    },
    // ===== 真后端 E2E =====
    {
      name: 'real',
      testDir: './e2e/real',
      // 真后端为单进程单库，用例间共享状态，串行跑避免相互干扰
      fullyParallel: false,
      workers: 1,
      use: {
        ...devices['Desktop Chrome'],
        baseURL: `http://localhost:${REAL_PORT}`,
      },
    },
  ],

  // 按当前选定的 project 起对应 webServer（Playwright 会启动数组里所有匹配 url 的服务）。
  // 用 --project 过滤时，Playwright 仍会尝试启动全部 webServer；为避免 mock 跑时白等真后端构建，
  // 我们用 PW_TARGET 环境变量在脚本里只挑选需要的 webServer（见 test:e2e / test:e2e:real 脚本）。
  webServer: buildWebServers(),
})

// 按 PW_TARGET 选择要启动的 webServer：'mock' 只起 vite mock；'real' 只起真后端二进制。
// 未设则两者都起（直接 `playwright test` 时的兜底，少用）。
function buildWebServers() {
  const target = process.env.PW_TARGET
  const servers: Array<Record<string, unknown>> = []

  if (target !== 'real') {
    servers.push({
      // 起假后端前端开发服务器（vite mock 模式，VITE_USE_MOCK=true 由 .env.mock 注入）
      command: `pnpm exec vite --mode mock --port ${MOCK_PORT} --strictPort`,
      url: `http://localhost:${MOCK_PORT}`,
      reuseExistingServer: !process.env.CI,
      timeout: 120_000,
    })
  }

  if (target === 'real') {
    servers.push({
      // 起真控制面二进制（sqlite + 固定凭据 + 固定端口）；二进制由 globalSetup 预先构建。
      // 工作目录用独立 e2e 运行目录，sqlite 库与首启 config.yml 落在那里，不污染仓库根。
      command: process.platform === 'win32' ? `"${beaconBin}"` : beaconBin,
      url: `http://localhost:${REAL_PORT}/admin/v1/namespaces`,
      cwd: fileURLToPath(new URL('./.e2e-real-run', import.meta.url)),
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
      env: {
        // 监听固定端口（与 baseURL 一致）
        BEACON_HTTP_ADDR: `:${REAL_PORT}`,
        // sqlite 开发模式，零外部依赖
        BEACON_DB_DRIVER: 'sqlite',
        BEACON_DB_DSN: 'beacon-e2e.db',
        // 固定 admin 凭据 + 签名密钥（仅本地 E2E）
        BEACON_ADMIN_PASSWORD: REAL_ADMIN_PASSWORD,
        BEACON_AUTH_SECRET: REAL_AUTH_SECRET,
        BEACON_BOOTSTRAP_TOKEN: 'beacon-e2e-bootstrap-token',
        BEACON_LOG_LEVEL: 'INFO',
      },
    })
  }

  return servers
}

// 导出给 globalSetup / 步骤复用的常量
export const E2E_PATHS = { repoRoot, beaconBin }
