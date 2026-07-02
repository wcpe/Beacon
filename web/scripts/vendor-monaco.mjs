// 将本地 monaco-editor 的 AMD min 构建（min/vs）连同中文语言包 vendored 到 public/monaco/vs，
// 由 vite 一并产出到 dist、再经控制面 go:embed 内嵌进单二进制 —— 使编辑器「离线可用」，
// 不再运行期依赖外网 CDN（jsdelivr）；同时修正官方 0.55.1 中文语言包的加载缺陷以启用中文 UI。
//
// 生成物（public/monaco/）已在 .gitignore 中忽略：不入库二进制，dev / build 前自动重生成（幂等）。
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const webRoot = join(here, '..')
const monacoRoot = join(webRoot, 'node_modules', 'monaco-editor')
const srcVs = join(monacoRoot, 'min', 'vs')
const destDir = join(webRoot, 'public', 'monaco')
const destVs = join(destDir, 'vs')
const marker = join(destDir, '.monaco-version')

// 未安装 monaco-editor → 明确报错，避免后续编辑器静默不可用
if (!existsSync(srcVs)) {
  console.error('[vendor-monaco] 未找到 node_modules/monaco-editor/min/vs，请先安装依赖（pnpm install）')
  process.exit(1)
}

const version = JSON.parse(readFileSync(join(monacoRoot, 'package.json'), 'utf8')).version

// 已是目标版本则跳过：拷贝 16M 资产较慢，避免每次 dev / build 重复执行
if (existsSync(marker) && readFileSync(marker, 'utf8').trim() === version && existsSync(join(destVs, 'loader.js'))) {
  console.log(`[vendor-monaco] 已是 ${version}，跳过`)
  process.exit(0)
}

// 重新拷贝整份 min/vs（含 editor 核心、各语言分块、assets 下的 worker）
rmSync(destVs, { recursive: true, force: true })
mkdirSync(destDir, { recursive: true })
cpSync(srcVs, destVs, { recursive: true })

// 修正中文语言包：官方 0.55.1 的 min 构建里中文包存在两处不一致，导致按 availableLanguages 加载时 require 永不 resolve、
// 编辑器卡在「加载中」：
//   1) 文件名是双后缀 nls.messages.zh-cn.js.js；
//   2) 内部 define 的模块 id 是 "vs/nls.messages.zh-cn.js"。
// 而 nls.messages-loader 实际请求的模块 id 是 "vs/nls.messages.zh-cn"（对应 URL nls.messages.zh-cn.js）。
// 这里把语言包另存为 nls.messages.zh-cn.js，并将 define id 改回 "vs/nls.messages.zh-cn"，使其可被正确加载
//（语言包通过 globalThis._VSCODE_NLS_MESSAGES 注入文案，只需被成功执行即可生效）。
const localeSrc = join(srcVs, 'nls.messages.zh-cn.js.js')
if (existsSync(localeSrc)) {
  const code = readFileSync(localeSrc, 'utf8').replace('define("vs/nls.messages.zh-cn.js"', 'define("vs/nls.messages.zh-cn"')
  writeFileSync(join(destVs, 'nls.messages.zh-cn.js'), code)
  // 删除无用的双后缀副本，避免误导
  rmSync(join(destVs, 'nls.messages.zh-cn.js.js'), { force: true })
}

writeFileSync(marker, `${version}\n`)
console.log(`[vendor-monaco] 已 vendored monaco ${version} → public/monaco/vs（含中文语言包）`)
