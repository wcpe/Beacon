package top.wcpe.beacon.agent.core.filetree

import top.wcpe.beacon.agent.core.platform.PlatformAdapter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * FileTreeApplier 自我保护单测：拒绝写入 agent 自身 plugin 目录下的文件。
 *
 * 背景：通道B 镜像根 = plugins/，控制面若误把 BeaconAgent/config.yml 等放进有效树（或经 FR-38 导入接受），
 * 默认行为是按相对路径原子覆写到 plugins/BeaconAgent/，会污染 agent 自身配置 / 快照（FR-41 env 把
 * 身份等字段拉回正确值的设计基于"agent 配置不被自己管"的隐含约定）。本测试锁定：
 * 顶段属于受保护集合时 applier 跳过该 path（不取、不写、不删），不影响合法条目落盘与清单更新。
 */
class FileTreeApplierSelfProtectionTest {

    private val root: File = Files.createTempDirectory("beacon-applier-self").toFile()
    private val mirrorWriter = FileMirrorWriter(root)
    private val manifestFile = File(root, "_applied.json")
    private val appliedStore = AppliedFileManifestStore(manifestFile, MinimalJsonCodec())
    private val warnings = mutableListOf<String>()
    private val adapter = WarnCapturingAdapter(root, warnings)

    @AfterTest
    fun cleanup() {
        root.deleteRecursively()
    }

    @Test
    fun `受保护顶段路径跳过、不写本地、合法条目正常落盘`() {
        val protectedSegments = setOf("BeaconAgent")
        val fetched = mutableListOf<String>()
        val applier = FileTreeApplier(
            mirrorWriter = mirrorWriter,
            appliedStore = appliedStore,
            adapter = adapter,
            // 记录哪些 path 被请求取内容——受保护项不应被请求
            fetchContent = { p ->
                fetched += p
                FileContent(p, "md5-${p.hashCode()}", "content-of-$p")
            },
            protectedSegments = protectedSegments,
        )

        val manifest = FileManifest(
            namespace = "prod", serverId = "lobby-1", group = "area1", zone = "z1",
            fileTreeMd5 = "tree-md5-1",
            entries = listOf(
                FileManifestEntry("BeaconAgent/config.yml", "m1"),
                FileManifestEntry("LuckPerms/config.yml", "m2"),
                FileManifestEntry("BeaconAgent/sub/effective.snapshot.json", "m3"),
            ),
        )

        val ok = applier.apply(manifest)
        assertTrue(ok, "合法条目能落盘即整轮收敛")

        // 受保护路径既未被请求取内容、也未落盘
        assertFalse(fetched.contains("BeaconAgent/config.yml"))
        assertFalse(fetched.contains("BeaconAgent/sub/effective.snapshot.json"))
        assertFalse(File(root, "BeaconAgent/config.yml").exists())
        assertFalse(File(root, "BeaconAgent/sub/effective.snapshot.json").exists())

        // 合法条目正常落盘
        assertTrue(fetched.contains("LuckPerms/config.yml"))
        assertTrue(File(root, "LuckPerms/config.yml").exists())

        // 告警包含被拦截的 path（便于运维核对）
        assertTrue(
            warnings.any { it.contains("BeaconAgent/config.yml") },
            "应为受保护路径打出 WARN，实际：$warnings",
        )

        // 清单已更新（即便有受保护项被跳过，整轮仍视为收敛）
        assertEquals("tree-md5-1", appliedStore.read()?.fileTreeMd5)
    }

    @Test
    fun `保护集合为空时不拦截任何路径（兼容旧装配）`() {
        val applier = FileTreeApplier(
            mirrorWriter = mirrorWriter,
            appliedStore = appliedStore,
            adapter = adapter,
            fetchContent = { p -> FileContent(p, "md5", "x") },
            protectedSegments = emptySet(),
        )

        val manifest = FileManifest(
            namespace = "prod", serverId = "lobby-1", group = "area1", zone = "z1",
            fileTreeMd5 = "tree-md5-2",
            entries = listOf(FileManifestEntry("BeaconAgent/config.yml", "m1")),
        )

        assertTrue(applier.apply(manifest))
        // 没有保护集合时回到旧语义：路径合法即写
        assertTrue(File(root, "BeaconAgent/config.yml").exists())
    }

    /** 最小 PlatformAdapter：runAsync 同步执行（单测无需真异步），WARN 文案落入 sink，其余 no-op。 */
    private class WarnCapturingAdapter(
        private val folder: File,
        private val sink: MutableList<String>,
    ) : PlatformAdapter {
        override fun runAsync(task: () -> Unit) = task()
        override fun runAsyncDelayed(delayMs: Long, task: () -> Unit) = task()
        override fun runSync(task: () -> Unit) = task()
        override fun dataFolder(): File = folder
        override fun publishConfigChanged(changed: Set<String>, newMd5: String) {}
        override fun info(msg: String) {}
        override fun warn(msg: String) { sink += msg }
        override fun error(msg: String, t: Throwable?) {}
    }

    /**
     * 最小 JsonCodec：把泛型树以 Java toString 形式编码、解码时 best-effort 还原 fileTreeMd5 / entries。
     * 本测试只用 AppliedFileManifestStore 的写 + 读出 fileTreeMd5，简单字符串编解析即足够，
     * 不引入 agent-adapters 的 kotlinx 依赖。
     */
    private class MinimalJsonCodec : JsonCodec {
        override fun encode(value: Any?): String {
            val tree = value as Map<*, *>
            val md5 = tree["fileTreeMd5"] as String
            @Suppress("UNCHECKED_CAST")
            val entries = tree["entries"] as List<Map<String, String>>
            val items = entries.joinToString(",") { """{"path":"${it["path"]}","md5":"${it["md5"]}"}""" }
            return """{"fileTreeMd5":"$md5","entries":[$items]}"""
        }

        override fun decode(json: String): Any? {
            // 简单解析：抓 fileTreeMd5 与 entries 数组（仅本测试断言用 fileTreeMd5，entries 容错为空）。
            val md5 = Regex("\"fileTreeMd5\":\"([^\"]*)\"").find(json)?.groupValues?.get(1) ?: ""
            val entryPattern = Regex("\\{\"path\":\"([^\"]*)\",\"md5\":\"([^\"]*)\"}")
            val entries = entryPattern.findAll(json)
                .map { mapOf("path" to it.groupValues[1], "md5" to it.groupValues[2]) }
                .toList()
            return mapOf("fileTreeMd5" to md5, "entries" to entries)
        }
    }
}
