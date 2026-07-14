package top.wcpe.beacon.agent.core.command

import java.io.File
import java.io.IOException
import java.io.InputStream
import java.nio.file.Files
import java.nio.file.LinkOption
import java.nio.file.Path
import java.security.MessageDigest

/**
 * 扫描服务器工作目录生成文件资产清单的 FS 边界（FR-163，见 ADR asset-manifest-sync-protocol）。
 *
 * 纯 java.nio（非平台 API），core 可直接调用；由 [top.wcpe.beacon.agent.core.lifecycle.AssetScanCoordinator]
 * 在 async 线程触发（读盘 + 分块哈希是阻塞 IO，绝不上 MC 主线程）。**只读、不写盘。**
 *
 * 扫描范围（规格 §4.1，相对路径相对**服务器工作目录**、正斜杠分隔）：
 * - `plugins/` 整棵子树（递归）；
 * - 服务器根目录顶层配置文件白名单 [ROOT_WHITELIST]（存在才收、不递归根目录）。
 * jar 属清单内容（哈希 / 大小 / mtime 全收，是跨服比对与 P9 差异扫描的核心对象），只是不可预览内容——故**不排除 jar**。
 *
 * FS 级安全（沿用 [PluginsTreeReader] 口径）：以 serverRoot 真实规范化路径（[Path.toRealPath]）为基准，逐个候选解析真实路径
 * 必须仍落在 serverRoot 之内——根除符号链接逃逸；不跟随目录符号链接下降（防链接环）；单文件 IOException 跳过该文件、**绝不整批失败**。
 *
 * 默认排除（大小写不敏感，作用于相对路径，规格 §4.1）：任意层级的 `.log` / `.log.gz` / `.tmp` / `.lock` 文件、
 * `.git` / `logs` / `cache` / `crash-reports` 目录下的文件、以及 `.DS_Store`（见 [isExcludedAsset]）。
 *
 * 增量优化（规格 §4.2）：传入上次清单 [scan] 的 previous（path→entry），若某文件 (size, mtimeMs) 与上次一致且非 force，
 * 复用上次 sha256、不重读文件；force=true 则忽略缓存、全部重哈希。
 *
 * 文件数上限（规格 §4.1）：候选超 [AssetIndexLimits.MAX_FILES] 时按路径字节序取前 N 个、truncated=true。
 */
object AssetIndexReader {
    /** 服务器根目录顶层配置文件白名单（存在才收、不递归根目录），规格 §4.1。 */
    private val ROOT_WHITELIST: List<String> =
        listOf(
            "server.properties",
            "bukkit.yml",
            "spigot.yml",
            "paper-global.yml",
            "paper-world-defaults.yml",
            "config.yml",
            "velocity.toml",
            "waterfall.yml",
        )

    /**
     * 扫描 serverRoot 生成资产清单。
     *
     * @param serverRoot     服务器工作目录（agent 壳层：pluginsBaseFolder 的父目录）
     * @param previous       上次成功上报的清单（path→entry），用于 (size,mtime) 命中复用 sha256；无缓存传空 map
     * @param force          true=忽略 mtime 缓存全部重哈希
     * @param selfExcludeDir agent 自身数据目录（如 plugins/BeaconAgent）：整棵排除，不纳入清单。**否则会因自写缓存 /
     *                       快照（asset-manifest.json、candidates-snapshot.json 等每周期 / 每 10s 变）自我指涉、致清单永不收敛**；
     *                       亦与规格 §4.6「agent 身份文件与本地缓存目录」口径一致。为 null 时不排除（测试 / 无自身目录场景）
     * @return 清单条目（按 path 升序）+ 是否因超文件数上限被截断
     */
    fun scan(
        serverRoot: File,
        previous: Map<String, AssetEntry>,
        force: Boolean,
        selfExcludeDir: File? = null,
    ): AssetScanResult {
        if (!serverRoot.isDirectory) return AssetScanResult(emptyList(), truncated = false)
        // 取不到真实根路径 → 放弃整次扫描（宁可不扫也不越界）。
        val rootReal = resolveRealOrNull(serverRoot) ?: return AssetScanResult(emptyList(), truncated = false)
        val selfExcludeReal = resolveRealOrNull(selfExcludeDir)

        // 1) 收集候选（相对路径 → 文件），不读内容：plugins 子树 + 根白名单文件。
        val candidates = LinkedHashMap<String, File>()
        collectPluginsSubtree(serverRoot, rootReal, selfExcludeReal, candidates)
        collectRootWhitelist(serverRoot, rootReal, selfExcludeReal, candidates)

        // 2) 按路径字节序稳定排序（供确定性截断与稳定输出）；超文件数上限则截断。
        val sorted = candidates.entries.sortedBy { it.key }
        val truncated = sorted.size > AssetIndexLimits.MAX_FILES
        val kept = if (truncated) sorted.take(AssetIndexLimits.MAX_FILES) else sorted

        // 3) 逐个 stat + 哈希（命中 (size,mtime) 缓存且非 force 复用上次 sha256）；单文件 IO 失败跳过。
        val entries = ArrayList<AssetEntry>(kept.size)
        for ((relative, file) in kept) {
            val entry = buildEntry(relative, file, previous, force) ?: continue
            entries.add(entry)
        }
        return AssetScanResult(entries, truncated)
    }

    /** 递归收集 `plugins/` 子树下的普通文件为候选。 */
    private fun collectPluginsSubtree(
        serverRoot: File,
        rootReal: Path,
        selfExcludeReal: Path?,
        out: MutableMap<String, File>,
    ) {
        val pluginsDir = File(serverRoot, "plugins")
        if (!pluginsDir.isDirectory) return
        walk(pluginsDir) { file ->
            if (file.isFile) addCandidate(file, rootReal, selfExcludeReal, out)
        }
    }

    /** 收集服务器根目录顶层白名单配置文件（存在才收、不递归根目录）为候选。 */
    private fun collectRootWhitelist(
        serverRoot: File,
        rootReal: Path,
        selfExcludeReal: Path?,
        out: MutableMap<String, File>,
    ) {
        for (name in ROOT_WHITELIST) {
            val file = File(serverRoot, name)
            if (file.isFile) addCandidate(file, rootReal, selfExcludeReal, out)
        }
    }

    /** 解析真实路径 + 符号链接逃逸剔除 + agent 自身目录排除 + 相对化正斜杠 + 默认排除规则；通过则加入候选。 */
    private fun addCandidate(
        file: File,
        rootReal: Path,
        selfExcludeReal: Path?,
        out: MutableMap<String, File>,
    ) {
        val relative = relativeInScope(file, rootReal, selfExcludeReal) ?: return
        if (!isExcludedAsset(relative)) out[relative] = file
    }

    /**
     * 候选文件的 serverRoot 相对路径（正斜杠）：真实路径须落在 serverRoot 内、且不在 agent 自身目录内，否则返回 null（剔除）。
     */
    private fun relativeInScope(
        file: File,
        rootReal: Path,
        selfExcludeReal: Path?,
    ): String? {
        val fileReal = resolveRealOrNull(file)
        if (fileReal == null || !withinScope(fileReal, rootReal, selfExcludeReal)) return null
        return rootReal.relativize(fileReal).joinToString("/") { it.toString() }.ifEmpty { null }
    }

    /** 解析目录 / 文件真实规范化路径；为 null 或解析失败返回 null。 */
    private fun resolveRealOrNull(dir: File?): Path? {
        if (dir == null) return null
        return try {
            dir.toPath().toRealPath()
        } catch (e: IOException) {
            null
        }
    }

    /** 构建单个清单条目：命中 (size,mtime) 缓存且非 force 复用上次 sha256，否则分块重哈希。IO 失败返回 null。 */
    private fun buildEntry(
        relative: String,
        file: File,
        previous: Map<String, AssetEntry>,
        force: Boolean,
    ): AssetEntry? {
        val stat = statOrNull(file) ?: return null
        val cached = previous[relative]
        val sha256 = if (cached != null && canReuseHash(cached, stat, force)) cached.sha256 else sha256Hex(file)
        return if (sha256 == null) {
            null // 读盘哈希失败 → 跳过
        } else {
            AssetEntry(relative, sha256, stat.size, stat.mtimeMs, TextFileHeuristic.looksTextByName(relative))
        }
    }

    /** stat 取文件字节大小 + 修改时间（UTC epoch 毫秒）；失败返回 null（best-effort，不整批失败）。 */
    private fun statOrNull(file: File): FileStat? {
        return try {
            val path = file.toPath()
            FileStat(Files.size(path), Files.getLastModifiedTime(path).toMillis())
        } catch (e: IOException) {
            null
        }
    }

    /** 分块（128 KiB）读文件算 sha256 小写 hex；读失败返回 null。 */
    private fun sha256Hex(file: File): String? {
        return try {
            val digest = MessageDigest.getInstance("SHA-256")
            file.inputStream().use { input -> updateDigest(digest, input) }
            digest.digest().joinToString("") { "%02x".format(it) }
        } catch (e: IOException) {
            null
        }
    }

    /**
     * 深度优先遍历 root 下所有项，对每个文件系统项回调 [onFile]（含目录，由回调自行判类型）。
     *
     * 不跟随目录符号链接下降（[LinkOption.NOFOLLOW_LINKS] 判定目录性），避免符号链接环导致无限递归。
     */
    private fun walk(
        root: File,
        onFile: (File) -> Unit,
    ) {
        val stack = ArrayDeque<File>()
        stack.addLast(root)
        while (stack.isNotEmpty()) {
            val dir = stack.removeLast()
            val children = dir.listFiles() ?: continue
            for (child in children) {
                val isDir = Files.isDirectory(child.toPath(), LinkOption.NOFOLLOW_LINKS)
                if (isDir) stack.addLast(child) else onFile(child)
            }
        }
    }
}

/** 默认排除的文件名后缀（小写）：任意层级的 .log / .log.gz / .tmp / .lock 文件。 */
private val EXCLUDED_ASSET_SUFFIXES: List<String> = listOf(".log", ".log.gz", ".tmp", ".lock")

/** 默认排除的目录段名（小写）：.git / logs / cache / crash-reports 目录下的文件整体排除。 */
private val EXCLUDED_ASSET_DIR_SEGMENTS: Set<String> = setOf(".git", "logs", "cache", "crash-reports")

/** 默认排除的确切文件名（小写）：.DS_Store。 */
private const val EXCLUDED_ASSET_DS_STORE: String = ".ds_store"

/**
 * 相对路径是否命中任一默认排除规则（大小写不敏感，规格 §4.1）。
 *
 * 文件名后缀命中 [EXCLUDED_ASSET_SUFFIXES]（.log 等）、或文件名为 [EXCLUDED_ASSET_DS_STORE]（.DS_Store）、
 * 或目录段（末段为文件名不计）命中 [EXCLUDED_ASSET_DIR_SEGMENTS]（logs 等目录）即排除。
 */
private fun isExcludedAsset(relative: String): Boolean {
    val lower = relative.lowercase()
    val fileName = lower.substringAfterLast('/')
    if (fileName == EXCLUDED_ASSET_DS_STORE || EXCLUDED_ASSET_SUFFIXES.any { fileName.endsWith(it) }) return true
    // 末段是文件名不算目录段：仅排除 logs 等目录下的文件，不排除名为 logs 的文件本身。
    return lower.split('/').dropLast(1).any { it in EXCLUDED_ASSET_DIR_SEGMENTS }
}

/** 候选真实路径是否在 serverRoot 内、且不在 agent 自身数据目录内（防自我指涉致清单不收敛）。 */
private fun withinScope(
    fileReal: Path,
    rootReal: Path,
    selfExcludeReal: Path?,
): Boolean {
    if (!fileReal.startsWith(rootReal)) return false // 逃逸 serverRoot（符号链接指向外部）→ 剔除
    return selfExcludeReal == null || !fileReal.startsWith(selfExcludeReal) // agent 自身数据目录整棵排除
}

/** (size,mtime) 与上次一致且非 force 则可复用上次哈希、不重读文件（规格 §4.2）。 */
private fun canReuseHash(
    cached: AssetEntry,
    stat: FileStat,
    force: Boolean,
): Boolean {
    if (force) return false
    return cached.size == stat.size && cached.mtimeMs == stat.mtimeMs
}

/** 逐 128 KiB 块喂 MessageDigest，直至流末（绝不整文件载内存）。 */
private fun updateDigest(
    digest: MessageDigest,
    input: InputStream,
) {
    val buffer = ByteArray(AssetIndexLimits.HASH_CHUNK_BYTES)
    while (true) {
        val n = input.read(buffer)
        if (n < 0) break
        digest.update(buffer, 0, n)
    }
}

/** stat 快照（文件字节大小 + 修改时间 UTC epoch 毫秒）。 */
private data class FileStat(
    val size: Long,
    val mtimeMs: Long,
)

/**
 * [AssetIndexReader.scan] 的结果。
 *
 * @param entries   清单条目（按 path 升序）
 * @param truncated 是否因超单服文件数上限（[AssetIndexLimits.MAX_FILES]）被截断
 */
data class AssetScanResult(
    val entries: List<AssetEntry>,
    val truncated: Boolean,
)
