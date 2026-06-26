package top.wcpe.beacon.agent.core.browse

import top.wcpe.beacon.agent.core.command.PluginsPathGuard
import top.wcpe.beacon.agent.core.command.TextFileHeuristic
import java.io.File
import java.io.IOException
import java.nio.ByteBuffer
import java.nio.charset.CodingErrorAction
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.LinkOption
import java.nio.file.Path

/**
 * 只读交互式文件浏览的 FS 边界（FR-109，见 ADR-0049）。纯 java.nio（非平台 API），core 可用；
 * 由平台壳的 [top.wcpe.beacon.agent.core.platform.PlatformAdapter] 用各自 `plugins/` 根委托调用。
 *
 * 三个**只读**惰加载原语（ADR-0049 决策 1）：
 * - [listDir]：懒列某目录的直接子项（分页，非递归整树）；
 * - [readTree]：按需展开某子树（逐层有界：深度上限 + 节点上限）；
 * - [readFile]：读单文本文件内容（受单文件上限，排除 jar / 二进制）。
 *
 * 只读、不写盘；**仅在 async 线程调用**（读盘是阻塞 IO，绝不上 MC 主线程，ADR-0049 决策 5）。
 *
 * 安全（限死真实 `plugins/` 根内，ADR-0049 决策 2-3，与 [top.wcpe.beacon.agent.core.command.PluginsTreeReader] 同源）：
 * - 字符串级前置闸：相对路径先过 [PluginsPathGuard]（拒 `..` / 绝对 / 反斜杠 / 冒号 / UNC / 保留名 / 段尾点空格）；
 * - Path 级容纳：以 root 的**真实规范化路径**（[Path.toRealPath]，解析符号链接）为基准，候选解析真实路径后**必须仍 startsWith rootReal**——根除符号链接逃逸；
 * - 不跟随目录符号链接下降（[LinkOption.NOFOLLOW_LINKS] 判目录性，防链接环）。
 * 任一校验失败即拒该次请求：列目录得 null、读文件得 null、不读不回传。
 */
object FsBrowseReader {

    /** 单文件读取上限 + 1 字节：让超限文件读到溢出量即可判 truncated，绝不全载。 */
    private const val PER_FILE_READ_CAP: Long = FsBrowseLimits.MAX_FILE_BYTES + 1

    /**
     * 懒列 [relPath]（相对 root）目录的**直接子项**，分页返回（FR-109 原语①）。
     *
     * [relPath] 为空串表示列 root 自身。返回 null 表示：root 非目录 / 路径越权 / 目标不存在或非目录。
     * 子项稳定排序（目录优先 + 名称升序），按 [offset]/[limit] 切片；[limit] 收口到 [FsBrowseLimits.MAX_LIST_LIMIT]。
     */
    fun listDir(root: File, relPath: String, offset: Int, limit: Int): DirListing? {
        val rootReal = realRootOrNull(root) ?: return null
        val target = resolveWithinRoot(rootReal, relPath) ?: return null
        // 目标须是真实目录（不跟随符号链接判目录性，防链接环）。
        if (!Files.isDirectory(target, LinkOption.NOFOLLOW_LINKS)) return null

        val children = target.toFile().listFiles() ?: emptyArray()
        // 稳定排序：目录优先，再按名称升序（跨平台一致、可复现，便于分页与测试）。
        val sorted = children
            .mapNotNull { child -> toEntry(rootReal, child) }
            .sortedWith(compareByDescending<BrowseEntry> { it.dir }.thenBy { it.name })

        val total = sorted.size
        val safeLimit = limit.coerceIn(1, FsBrowseLimits.MAX_LIST_LIMIT)
        val safeOffset = offset.coerceAtLeast(0)
        val page = if (safeOffset >= total) {
            emptyList()
        } else {
            sorted.subList(safeOffset, minOf(safeOffset + safeLimit, total))
        }
        return DirListing(
            path = relativeOf(rootReal, target),
            entries = page,
            offset = safeOffset,
            limit = safeLimit,
            total = total,
            hasMore = safeOffset + page.size < total,
        )
    }

    /**
     * 按需展开 [relPath]（相对 root）起的子树（FR-109 原语②）：逐层有界，非整盘一次拉全。
     *
     * 受 [maxDepth]（收口到 [FsBrowseLimits.MAX_TREE_DEPTH]）与 [FsBrowseLimits.MAX_TREE_NODES] 节点上限约束：
     * 超深度 / 超节点上限的目录 `truncated=true`、children 空（前端可继续懒列）。
     * 返回 null 表示：root 非目录 / 路径越权 / 目标不存在或非目录。
     */
    fun readTree(root: File, relPath: String, maxDepth: Int): TreeNode? {
        val rootReal = realRootOrNull(root) ?: return null
        val target = resolveWithinRoot(rootReal, relPath) ?: return null
        if (!Files.isDirectory(target, LinkOption.NOFOLLOW_LINKS)) return null

        val depthCap = maxDepth.coerceIn(0, FsBrowseLimits.MAX_TREE_DEPTH)
        val budget = NodeBudget(FsBrowseLimits.MAX_TREE_NODES)
        return buildNode(rootReal, target, depthCap, budget)
    }

    /**
     * 读单文本文件内容（FR-109 原语③）。
     *
     * 排除目录 / `.jar` / 二进制（NUL 或非法 UTF-8）；超单文件上限只读前缀、`truncated=true`，绝不全载。
     * 返回 null 表示：路径越权 / 不存在 / 非普通文件 / jar / 二进制 / 读失败。
     */
    fun readFile(root: File, relPath: String): FileContent? {
        if (relPath.isEmpty()) return null // 根不是文件
        val rootReal = realRootOrNull(root) ?: return null
        val target = resolveWithinRoot(rootReal, relPath) ?: return null
        // 必须是真实普通文件（非目录 / 非目录符号链接 / 非设备文件）。
        if (!Files.isRegularFile(target, LinkOption.NOFOLLOW_LINKS)) {
            // 允许「指向 root 内普通文件」的符号链接：再按真实路径判普通文件性。
            if (!Files.isRegularFile(target)) return null
        }
        val file = target.toFile()
        if (file.name.lowercase().endsWith(".jar")) return null // 排除 jar（最大二进制来源）

        val raw = readCapped(file) ?: return null
        val truncated = raw.size.toLong() > FsBrowseLimits.MAX_FILE_BYTES
        // 截断到上限再解码（避免把溢出的 1 字节误判破坏 UTF-8 边界）。
        val effective = if (truncated) raw.copyOf(FsBrowseLimits.MAX_FILE_BYTES.toInt()) else raw
        val text = decodeUtf8OrNull(effective) ?: return null // 二进制 → 不读
        return FileContent(
            path = relativeOf(rootReal, target),
            content = text,
            truncated = truncated,
        )
    }

    // ---- 内部：路径解析与安全校验 ----

    /** 取 root 真实规范化路径；非目录 / 解析失败返回 null（宁可不读也不越界）。 */
    private fun realRootOrNull(root: File): Path? {
        if (!root.isDirectory) return null
        return try {
            root.toPath().toRealPath()
        } catch (e: IOException) {
            null
        }
    }

    /**
     * 把相对路径 [relPath] 解析到 root 真实路径下的目标 Path，并校验**仍落在 root 内**；越权返回 null。
     *
     * 双保险（ADR-0049 决策 3）：① 字符串级前置闸 [PluginsPathGuard]（空串=列根除外）；
     * ② Path 级——拼接后 `normalize()` 仍 startsWith [rootReal]，再解析真实路径（符号链接）后**仍 startsWith [rootReal]**。
     */
    private fun resolveWithinRoot(rootReal: Path, relPath: String): Path? {
        if (relPath.isEmpty()) return rootReal // 空串 = 列 root 自身
        // 字符串级前置闸：拒 `..` / 绝对 / 反斜杠 / 冒号 / UNC / 保留名 / 段尾点空格。
        if (!PluginsPathGuard.isSafe(relPath)) return null

        // Path 级拼接 + 规范化容纳（即便字符串闸放过，normalize 后也必须仍在根内）。
        val resolved = try {
            rootReal.resolve(relPath).normalize()
        } catch (e: Exception) {
            return null
        }
        if (!resolved.startsWith(rootReal)) return null

        // 解析真实路径（符号链接）后必须仍在根内——根除符号链接逃逸。
        // 目标可能尚不存在（理论上不该，浏览读已存在项）：解析失败即拒。
        val real = try {
            resolved.toRealPath()
        } catch (e: IOException) {
            return null
        }
        if (!real.startsWith(rootReal)) return null
        return real
    }

    /** 相对 root 真实路径的相对路径（正斜杠分隔；root 自身得空串）。 */
    private fun relativeOf(rootReal: Path, target: Path): String {
        val rel = rootReal.relativize(target)
        return rel.joinToString("/") { it.toString() }
    }

    /** 把一个文件系统子项转为 [BrowseEntry]；逃逸 root 的符号链接被剔除（返回 null）。 */
    private fun toEntry(rootReal: Path, child: File): BrowseEntry? {
        val isDir = Files.isDirectory(child.toPath(), LinkOption.NOFOLLOW_LINKS)
        // 解析真实路径判逃逸：指向 root 外的符号链接（含目录链接）剔除。
        val real = try {
            child.toPath().toRealPath()
        } catch (e: IOException) {
            return null // 坏链接 / 解析失败 → 不列出
        }
        if (!real.startsWith(rootReal)) return null
        val name = child.name
        return BrowseEntry(
            name = name,
            relPath = relativeOf(rootReal, real),
            dir = isDir,
            size = if (isDir) 0L else child.length(),
            text = if (isDir) false else TextFileHeuristic.looksTextByName(name),
        )
    }

    /** 递归构建子树节点，逐层有界（深度 + 全局节点预算）。 */
    private fun buildNode(rootReal: Path, dir: Path, remainingDepth: Int, budget: NodeBudget): TreeNode {
        val dirName = dir.fileName?.toString() ?: ""
        val base = TreeNode(
            name = dirName,
            relPath = relativeOf(rootReal, dir),
            dir = true,
            size = 0L,
            text = false,
            children = emptyList(),
            truncated = false,
        )
        // 深度耗尽：该目录不再展开，标 truncated（前端可继续懒列）。
        if (remainingDepth <= 0) return base.copy(truncated = true)

        val rawChildren = dir.toFile().listFiles() ?: return base
        val entries = rawChildren
            .mapNotNull { child -> toEntry(rootReal, child) }
            .sortedWith(compareByDescending<BrowseEntry> { it.dir }.thenBy { it.name })

        val children = ArrayList<TreeNode>(entries.size)
        var truncated = false
        for (entry in entries) {
            if (!budget.tryConsume()) {
                truncated = true // 节点预算耗尽：余项不再展开
                break
            }
            if (entry.dir) {
                val childPath = rootReal.resolve(entry.relPath)
                children.add(buildNode(rootReal, childPath, remainingDepth - 1, budget))
            } else {
                children.add(
                    TreeNode(
                        name = entry.name,
                        relPath = entry.relPath,
                        dir = false,
                        size = entry.size,
                        text = entry.text,
                        children = emptyList(),
                        truncated = false,
                    ),
                )
            }
        }
        return base.copy(children = children, truncated = truncated)
    }

    /** 读文件内容，最多 [PER_FILE_READ_CAP] 字节；读失败返回 null。 */
    private fun readCapped(file: File): ByteArray? {
        return try {
            file.inputStream().use { input ->
                val buffer = java.io.ByteArrayOutputStream()
                val chunk = ByteArray(8192)
                var remaining = PER_FILE_READ_CAP
                while (remaining > 0) {
                    val toRead = minOf(chunk.size.toLong(), remaining).toInt()
                    val n = input.read(chunk, 0, toRead)
                    if (n < 0) break
                    buffer.write(chunk, 0, n)
                    remaining -= n
                }
                buffer.toByteArray()
            }
        } catch (e: IOException) {
            null
        }
    }

    /** 严格按 UTF-8 解码；含 NUL 或非法 UTF-8 视作二进制返回 null（与反向抓取同口径）。 */
    private fun decodeUtf8OrNull(bytes: ByteArray): String? {
        for (b in bytes) {
            if (b.toInt() == 0) return null // NUL 字节 → 二进制
        }
        return try {
            val decoder = StandardCharsets.UTF_8.newDecoder()
                .onMalformedInput(CodingErrorAction.REPORT)
                .onUnmappableCharacter(CodingErrorAction.REPORT)
            decoder.decode(ByteBuffer.wrap(bytes)).toString()
        } catch (e: java.nio.charset.CharacterCodingException) {
            null // 非合法 UTF-8 → 二进制
        }
    }

    /** 子树展开的全局节点预算（逐层共享，达上限即停止再收）。 */
    private class NodeBudget(private var remaining: Int) {
        /** 尝试消费一个节点配额；返回 false 表示已耗尽。 */
        fun tryConsume(): Boolean {
            if (remaining <= 0) return false
            remaining--
            return true
        }
    }
}
