#!/usr/bin/env node
/**
 * 冻结一个文档版本，并保证快照只包含「面向使用者」的文档。
 *
 * 为什么需要这个脚本：Docusaurus 的 `docs:version` 会**整个复制** `docs/` 目录，
 * 而本仓库的 `docs/` 同时存放内部文档（adr / specs / PRD / ROADMAP …）。
 * 直接使用原生命令会把内部文档一并复制进 `versioned_docs/`，既撑大仓库
 * （单次约 4 MB、261 文件），也会让内部内容经站点暴露。
 *
 * 本脚本 = 调用原生 `docs:version` + 按白名单裁剪快照 + 校验结果。
 *
 * 用法：
 *   pnpm docs:version:freeze 1.5.0
 *   （不带参数则读取仓库根 VERSION）
 */

import {execFileSync} from 'node:child_process'
import {existsSync, readFileSync, readdirSync, rmSync} from 'node:fs'
import {dirname, join, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const SITE_ROOT = resolve(HERE, '..')
const REPO_ROOT = resolve(SITE_ROOT, '..')

/** 快照中允许保留的条目（与 docusaurus.config.ts 的 include 白名单同源）。 */
const ALLOWED = new Set(['wiki', 'OPERATIONS.md', 'SDK.md', 'images'])

/** 内部编号形态：出现即视为内部内容混入。 */
const INTERNAL_NUMBERING = 'FR-[0-9]{2,}|ADR-[0-9]{3,}'

/** 从仓库根 VERSION 读取版本号。 */
function readRepoVersion() {
  return readFileSync(join(REPO_ROOT, 'VERSION'), 'utf8').trim()
}

/** 校验传入版本号是合法的 X.Y.Z。 */
function assertSemver(version) {
  if (!/^\d+\.\d+\.\d+$/.test(version)) {
    throw new Error(`版本号须为 X.Y.Z 形式（仓库 VERSION 为 ${readRepoVersion()}）：${version}`)
  }
}

/** 冻结版本：调用原生命令。 */
function freeze(version) {
  console.log(`[freeze] 冻结文档版本 ${version} …`)
  execFileSync('pnpm', ['exec', 'docusaurus', 'docs:version', version], {
    cwd: SITE_ROOT,
    stdio: 'inherit',
  })
}

/**
 * 裁剪快照：删除白名单之外的条目。
 * 内部文档不保留副本——它们仍在仓库 docs/ 中可读，站点不需要也不应包含它们。
 */
function prune(snapshotDir) {
  const removed = []
  for (const entry of readdirSync(snapshotDir)) {
    if (!ALLOWED.has(entry)) {
      rmSync(join(snapshotDir, entry), {recursive: true, force: true})
      removed.push(entry)
    }
  }
  return removed
}

/** 校验快照：白名单之外不得有内容；内部编号不得出现。 */
function verify(snapshotDir) {
  const unexpected = readdirSync(snapshotDir).filter((entry) => !ALLOWED.has(entry))
  if (unexpected.length > 0) {
    throw new Error(`快照仍含白名单外条目：${unexpected.join(', ')}`)
  }

  try {
    execFileSync('grep', ['-rlE', INTERNAL_NUMBERING, snapshotDir], {stdio: 'pipe'})
    // grep 有匹配即退出码 0：说明命中内部编号，属失败。
    throw new Error('快照中出现内部需求 / 决策编号')
  } catch (err) {
    // grep 无匹配时退出码为 1，属预期；其它错误向上抛。
    if (err.status !== 1) {
      throw err
    }
  }
}

function main() {
  const version = process.argv[2] || readRepoVersion()
  assertSemver(version)

  freeze(version)

  const snapshotDir = join(SITE_ROOT, 'versioned_docs', `version-${version}`)
  if (!existsSync(snapshotDir)) {
    throw new Error(`未找到快照目录：${snapshotDir}`)
  }

  const removed = prune(snapshotDir)
  if (removed.length > 0) {
    console.log(`[prune] 已从快照移除白名单外条目：${removed.join(', ')}`)
  }

  verify(snapshotDir)

  console.log(`[ok] 版本 ${version} 已冻结并通过校验：${snapshotDir}`)
  console.log('[提示] 若本次冻结对应正式发布，请同步更新 docusaurus.config.ts 中 current 的 label。')
}

main()
