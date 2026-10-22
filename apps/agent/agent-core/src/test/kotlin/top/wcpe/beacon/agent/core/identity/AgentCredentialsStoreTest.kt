package top.wcpe.beacon.agent.core.identity

import top.wcpe.beacon.agent.core.testsupport.TestJsonCodec
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.attribute.FileTime
import java.time.Instant
import java.util.UUID
import java.util.concurrent.CountDownLatch
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 身份凭据存储的测试（FR-276）。
 *
 * 重点覆盖三类**会静默出错**的场景：
 *  1. 凭据读写往返丢失字段（token 含特殊字符时尤甚）
 *  2. 损坏/未知版本文件被"猜着读"，把坏数据当成有效凭据
 *  3. 旧 identity.yml 迁移时身份漂移（会让控制面多出一个重复身份）
 *
 * 以及身份收敛后的三类新风险：解析身份不能覆盖已有凭据、迁移必须真落盘、
 * 绑定落盘要幂等（否则每次接入都抖盘）。
 *
 * 最后一段专门锁「不可读的凭据文件绝不能被静默覆盖」：只有「文件不存在」才是可以放心写的状态，
 * 坏文件必须**先备份再迁移**、高版本文件必须**拒绝迁移**并打 ERROR、解析身份时**一律不动**它。
 */
class AgentCredentialsStoreTest {
    private fun tempDir(): Path = Files.createTempDirectory("beacon-cred-test")

    /** 用作「文件未被重写」的证据：一个远离现在的时间戳，任何一次真实写入都会把它改掉。 */
    private val staleMarker: FileTime = FileTime.fromMillis(1_600_000_000_000)

    @Test
    fun `凭据写入后可原样读回`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val cred =
            AgentCredentials(
                identityId = "0f8fad5b-d9cb-469f-a165-70867728950e",
                endpoint = "https://beacon.example.com",
                token = "bn_abc123",
            )

        store.save(cred)
        val loaded = store.load()

        assertNotNull(loaded)
        assertEquals(cred.identityId, loaded.identityId)
        assertEquals(cred.endpoint, loaded.endpoint)
        assertEquals(cred.token, loaded.token)
        assertNull(loaded.binding)
        assertTrue(loaded.canConnect, "地址与令牌都在，应可直连")
    }

    @Test
    fun `令牌含引号与反斜杠也能原样读回`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        // 令牌里出现引号/反斜杠在真实场景里少见，但一旦出现必须不破坏 JSON 结构，
        // 否则解析会静默截断——表现为「token 莫名其妙变短」。
        val tricky = """bn_a"b\c"""
        store.save(
            AgentCredentials(
                identityId = "0f8fad5b-d9cb-469f-a165-70867728950e",
                endpoint = "http://127.0.0.1:20020",
                token = tricky,
            ),
        )

        val loaded = store.load()
        assertNotNull(loaded)
        assertEquals(tricky, loaded.token)
    }

    @Test
    fun `绑定事实可单独更新且不影响地址与令牌`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        store.save(
            AgentCredentials("0f8fad5b-d9cb-469f-a165-70867728950e", "https://b.example.com", "bn_x"),
        )

        val updated =
            store.updateBinding(
                AgentCredentials.BoundIdentity(
                    namespace = "prod",
                    serverId = "lobby-1",
                    kind = "bukkit",
                    address = "127.0.0.1:25565",
                    boundAt = null,
                ),
            )

        assertNotNull(updated)
        assertEquals("prod", updated.binding?.namespace)
        assertEquals("lobby-1", updated.binding?.serverId)
        // 关键：更新绑定不得把地址/令牌清空（那会让下次启动无法接入）。
        assertEquals("https://b.example.com", updated.endpoint)
        assertEquals("bn_x", updated.token)
    }

    @Test
    fun `损坏文件被判为无凭据而不是读出半截值`() {
        val dir = tempDir()
        Files.writeString(dir.resolve("agent-credentials.json"), "{ 这不是合法 JSON")
        val store = AgentCredentialsStore(dir, TestJsonCodec)

        // 关键：宁可当作「没凭据」让流程走 join，也不能读出半截数据把错误地址当成真的。
        assertNull(store.load())
    }

    @Test
    fun `未知格式版本被判为无凭据`() {
        val dir = tempDir()
        Files.writeString(
            dir.resolve("agent-credentials.json"),
            """{"formatVersion": 99, "identityId": "0f8fad5b-d9cb-469f-a165-70867728950e", "endpoint": "http://a", "token": "t"}""",
        )
        val store = AgentCredentialsStore(dir, TestJsonCodec)

        assertNull(store.load(), "未来版本的文件不该被旧 agent 猜着读")
    }

    @Test
    fun `旧身份文件迁移后身份标识不变`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val legacyId = "1f8fad5b-d9cb-469f-a165-70867728950e"

        val migrated = store.migrateFromLegacy(StoredAgentIdentity(legacyId, null, isValid = true))

        assertNotNull(migrated)
        // 身份不漂移是迁移的唯一目的：漂了就等于控制面上多一台新服务器。
        assertEquals(legacyId, migrated.identityId)
        assertFalse(migrated.canConnect, "迁移只搬身份，地址与令牌要等 join 或 config.yml")
    }

    @Test
    fun `已有凭据时不重复迁移`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        store.save(
            AgentCredentials("0f8fad5b-d9cb-469f-a165-70867728950e", "https://a", "bn_a"),
        )

        val result =
            store.migrateFromLegacy(
                StoredAgentIdentity("1f8fad5b-d9cb-469f-a165-70867728950e", null, isValid = true),
            )

        assertNull(result, "已有凭据时不得用旧身份覆盖")
        assertEquals("0f8fad5b-d9cb-469f-a165-70867728950e", store.load()?.identityId)
    }

    // ---- 身份收敛为单一真源（FR-276 #4）：解析优先级 / 迁移 / 幂等 ----

    @Test
    fun `凭据已存在时解析用凭据身份且不覆盖其余字段`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val storedId = "0f8fad5b-d9cb-469f-a165-70867728950e"
        store.save(AgentCredentials(storedId, "https://a", "bn_a"))

        val resolved =
            store.resolveIdentityId(StoredAgentIdentity("1f8fad5b-d9cb-469f-a165-70867728950e", null, isValid = true))

        // 旧文件里有另一个身份也不许抢：凭据是唯一真源，抢了就等于运维 join 过的身份白写了。
        assertEquals(storedId, resolved)
        val loaded = store.load()
        assertNotNull(loaded)
        assertEquals("bn_a", loaded.token, "解析身份不得动地址与令牌")
        assertEquals("https://a", loaded.endpoint)
    }

    @Test
    fun `只有旧身份文件时迁移并把凭据落盘`() {
        val dir = tempDir()
        // 用真实身份存储生成旧文件，连「旧文件格式」这层契约一起覆盖。
        val legacy = AgentIdentityStore(dir).loadOrCreate()
        assertTrue(legacy.isValid)
        val store = AgentCredentialsStore(dir, TestJsonCodec)

        val resolved = store.resolveIdentityId(legacy)

        assertEquals(legacy.identityId, resolved, "迁移的唯一目的就是身份不漂移")
        val persisted = store.load()
        assertNotNull(persisted, "迁移必须把身份写进凭据，否则下次启动还得再迁一次")
        assertEquals(legacy.identityId, persisted.identityId)
        assertFalse(persisted.canConnect, "迁移只搬身份，地址与令牌等 join 或 config.yml 补")
    }

    @Test
    fun `既无凭据也无旧身份时生成新身份并落盘`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)

        val resolved =
            store.resolveIdentityId(StoredAgentIdentity("", null, isValid = false, error = "身份文件不存在"))

        assertEquals(4, UUID.fromString(resolved).version(), "必须是 UUIDv4")
        assertEquals(resolved.lowercase(), resolved, "落盘的身份统一小写，避免大小写差异被当成两台机器")
        val persisted = store.load()
        assertNotNull(persisted, "生成的身份必须落盘，否则每次重启都会变成一台新服务器")
        assertEquals(resolved, persisted.identityId)
    }

    @Test
    fun `旧身份文件损坏时不挡凭据里的合法身份`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val storedId = "0f8fad5b-d9cb-469f-a165-70867728950e"
        store.save(AgentCredentials(storedId, "https://a", "bn_a"))
        Files.writeString(dir.resolve("identity.yml"), "format-version: 1\nidentity-id: not-a-uuid\n")
        val broken = AgentIdentityStore(dir).loadExisting()
        assertFalse(broken.isValid, "前置条件：旧文件确实被判为损坏")

        // 升级场景里旧文件可能被动过（拷贝、编码损坏），但凭据里的身份仍然可信，不该被它挡住。
        assertEquals(storedId, store.resolveIdentityId(broken))
    }

    @Test
    fun `解析身份幂等且第二次不重写文件`() {
        val dir = tempDir()
        val legacy = AgentIdentityStore(dir).loadOrCreate()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val first = store.resolveIdentityId(legacy)
        Files.setLastModifiedTime(store.filePath(), staleMarker)

        val second = store.resolveIdentityId(legacy)

        assertEquals(first, second, "同一台机器连续解析必须得到同一身份")
        assertEquals(staleMarker, Files.getLastModifiedTime(store.filePath()), "身份已存在时不得再写盘")
    }

    @Test
    fun `相同绑定不重复写盘`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        store.save(AgentCredentials("0f8fad5b-d9cb-469f-a165-70867728950e", "https://a", "bn_a"))
        val binding =
            AgentCredentials.BoundIdentity(
                namespace = "prod",
                serverId = "lobby-1",
                kind = "bukkit",
                address = "127.0.0.1:25565",
                boundAt = Instant.parse("2024-05-01T00:00:00Z"),
            )

        assertNotNull(store.updateBinding(binding))
        Files.setLastModifiedTime(store.filePath(), staleMarker)
        assertNotNull(store.updateBinding(binding))

        // 绑定每次接入确认都会到来，重复写盘只会让文件时间戳无谓抖动。
        assertEquals(staleMarker, Files.getLastModifiedTime(store.filePath()), "绑定没变就不该再写盘")
        assertEquals(binding, store.load()?.binding, "已存的绑定事实必须可原样读回（join 的拒绝守卫靠它）")
    }

    @Test
    fun `并发写凭据不会留下损坏文件`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val start = CountDownLatch(1)
        val workers =
            (1..THREADS).map { index ->
                Thread {
                    start.await()
                    repeat(ROUNDS) { round ->
                        store.save(
                            AgentCredentials("0f8fad5b-d9cb-469f-a165-70867728950e", "https://a", "bn_$index-$round"),
                        )
                    }
                }
            }
        workers.forEach { it.start() }
        start.countDown()
        workers.forEach { it.join() }

        // 固定 tmp 名时后一个写会截断前一个的临时文件，move 出来的可能是半截 JSON。
        val loaded = store.load()
        assertNotNull(loaded, "并发写后凭据仍须可解析")
        assertTrue(loaded.token.startsWith("bn_"), "读回的必须是某一次完整写入的值，而不是拼接残片")
        val leftovers = Files.list(dir).use { paths -> paths.filter { it.fileName.toString().endsWith(".tmp") }.count() }
        assertEquals(0, leftovers, "临时文件必须清理干净，不能污染数据目录")
    }

    // ---- 不可读的凭据文件不得被静默覆盖（M2）：只有「文件不存在」才可以放心写 ----

    /**
     * 凭据文件损坏 + 旧 identity.yml 合法 → 迁移**必须先备份**、再搬旧身份。
     *
     * 反例（曾经的实现）：`load()` 把「不存在」和「读不出来」都归成 null，迁移于是直接原子覆盖，
     * 原 endpoint / token 永久消失、身份被旧文件里的 ID 换掉，且无备份无告警。
     */
    @Test
    fun `凭据文件损坏时迁移先备份再搬旧身份`() {
        val dir = tempDir()
        val broken = "{ 这不是合法 JSON"
        Files.writeString(dir.resolve("agent-credentials.json"), broken)
        val warns = mutableListOf<String>()
        val store = AgentCredentialsStore(dir, TestJsonCodec, warn = warns::add)
        val legacyId = "1f8fad5b-d9cb-469f-a165-70867728950e"

        val migrated = store.migrateFromLegacy(StoredAgentIdentity(legacyId, null, isValid = true))

        assertNotNull(migrated)
        assertEquals(legacyId, migrated.identityId)
        // 坏文件必须留下来：里面可能还有运维唯一能抢救的 endpoint / token。
        val backups = corruptBackups(dir)
        assertEquals(1, backups.size, "损坏凭据必须先备份再迁移，实际目录：${listNames(dir)}")
        assertEquals(broken, Files.readString(backups.single()), "备份必须是原文件的逐字节副本")
        assertEquals(legacyId, store.load()?.identityId, "备份后凭据里应是迁移来的旧身份")
        assertTrue(warns.any { it.contains("备份") }, "备份 + 迁移必须显式告警，实际：$warns")
    }

    /**
     * 凭据由更高版本的 agent 写入 + 旧文件合法 → **拒绝迁移**、原文件逐字节不变、打 ERROR。
     *
     * 降级绝不能变成数据销毁：旧 agent 读不懂新字段，覆盖会让新版信息永久消失。
     */
    @Test
    fun `凭据版本不支持时拒绝迁移且不动原文件`() {
        val dir = tempDir()
        val fromFuture =
            """{"formatVersion": 99, "identityId": "0f8fad5b-d9cb-469f-a165-70867728950e", """ +
                """"endpoint": "https://kept.example.com", "token": "bn_keep"}"""
        val credentialsFile = dir.resolve("agent-credentials.json")
        Files.writeString(credentialsFile, fromFuture)
        val errors = mutableListOf<String>()
        val store = AgentCredentialsStore(dir, TestJsonCodec, warn = {}, error = errors::add)

        val migrated = store.migrateFromLegacy(StoredAgentIdentity("1f8fad5b-d9cb-469f-a165-70867728950e", null, isValid = true))

        assertNull(migrated, "高版本凭据不得被旧身份覆盖")
        assertEquals(fromFuture, Files.readString(credentialsFile), "拒绝迁移时原文件必须逐字节保持原样")
        assertEquals(1, errors.size, "拒绝迁移必须打 ERROR（不是静默跳过），实际：$errors")
        assertTrue(errors.single().contains("99"), "告警须点明读到的版本号，实际：${errors.single()}")
        assertTrue(corruptBackups(dir).isEmpty(), "文件没坏，不该产生备份")
        // 既有行为保持：读不懂的版本仍当作「无凭据」，不抛异常、不阻断启动。
        assertNull(store.load(), "读不懂的版本不该被猜着读")
    }

    /** 凭据不存在 → 正常迁移，且**不该**留下任何 `.corrupt-` 备份（没有东西可备份）。 */
    @Test
    fun `凭据不存在时正常迁移且不产生备份`() {
        val dir = tempDir()
        val warns = mutableListOf<String>()
        val store = AgentCredentialsStore(dir, TestJsonCodec, warn = warns::add)
        val legacyId = "1f8fad5b-d9cb-469f-a165-70867728950e"

        val migrated = store.migrateFromLegacy(StoredAgentIdentity(legacyId, null, isValid = true))

        assertEquals(legacyId, migrated?.identityId)
        assertEquals(legacyId, store.load()?.identityId)
        assertTrue(corruptBackups(dir).isEmpty(), "没有任何文件损坏，不该留下备份：${listNames(dir)}")
        // 只断言「没有损坏/备份类告警」：Windows 没有 POSIX 权限，0600 降级告警是正常的，
        // 把它一起断言进来会让这条用例在 Windows 上恒假。
        assertTrue(
            warns.none { it.contains("不可读") || it.contains(CORRUPT_MARK) },
            "正常迁移不该出现「损坏/备份」相关告警，实际：$warns",
        )
    }

    /**
     * 凭据不可读时 [AgentCredentialsStore.resolveIdentityId] **不覆盖**原文件。
     *
     * 这是 M2 最危险的一条：解析身份的分支里曾经无条件 `save(...)`——凭据被写坏的那一刻，
     * 一句「生成新身份并落盘」就把它彻底替换掉，agent 再也回不到已绑定状态。
     */
    @Test
    fun `凭据不可读时解析身份不覆盖原文件`() {
        val dir = tempDir()
        // 合法 JSON，但身份字段非法 → 属于「文件在、读不出来」，且里面还有可抢救的地址与令牌。
        val broken =
            """{"formatVersion": 1, "identityId": "not-a-uuid", """ +
                """"endpoint": "https://keep.example.com", "token": "bn_keep"}"""
        val credentialsFile = dir.resolve("agent-credentials.json")
        Files.writeString(credentialsFile, broken)
        val warns = mutableListOf<String>()
        val store = AgentCredentialsStore(dir, TestJsonCodec, warn = warns::add)
        val legacyId = "1f8fad5b-d9cb-469f-a165-70867728950e"

        val resolved = store.resolveIdentityId(StoredAgentIdentity(legacyId, null, isValid = true))

        assertEquals(legacyId, resolved, "凭据不可读时应沿用旧身份，而不是悄悄换成新身份")
        assertEquals(broken, Files.readString(credentialsFile), "不可读的凭据不许被覆盖")
        assertTrue(warns.any { it.contains("不可读") }, "凭据不可读属于降级，必须显式告警，实际：$warns")

        // 连旧身份都没有时也一样：宁可本次身份不落盘，也不能把原文件冲掉。
        val fresh = store.resolveIdentityId(StoredAgentIdentity("", null, isValid = false, error = "身份文件不存在"))
        assertEquals(4, UUID.fromString(fresh).version(), "没有旧身份时仍要能给出一个可用身份")
        assertEquals(broken, Files.readString(credentialsFile), "没有旧身份时同样不许覆盖不可读的凭据")
        assertNull(store.load(), "不可读的凭据仍按「无凭据」对外汇报（既有行为）")
    }

    /**
     * 凭据权威时，`identity.yml` 必须被**对齐成同一个 ID**，而不是留下一个休眠的第二个 UUID。
     *
     * ## 这条防的是什么
     *
     * 旧壳层在凭据已权威时仍调 `loadOrCreate()`：文件缺失就落盘一个从未在控制面注册过的
     * UUID B。正常启动用凭据里的 A（B 不参与），可一旦凭据变得不可读，迁移会立刻把 B
     * 复活成身份——控制面看到的是「又来了一台新服务器」。
     *
     * 故壳层的流程是「只读不新建（loadExisting）+ 对齐（alignTo）」：本用例锁住后半段，
     * 即两份文件最终一定指向同一个身份。
     */
    @Test
    fun `凭据权威时把旧身份文件对齐成同一 ID`() {
        val dir = tempDir()
        val identityStore = AgentIdentityStore(dir)
        // 前置条件：数据目录里已经有历史遗留的 identity.yml（升级前生成，从未参与接入）。
        val dormant = identityStore.loadOrCreate()
        val authoritativeId = "0f8fad5b-d9cb-469f-a165-70867728950e"
        val credentials = AgentCredentialsStore(dir, TestJsonCodec)
        credentials.save(AgentCredentials(authoritativeId, "https://a", "bn_a"))

        // 壳层新流程：只读旧文件（不在缺文件时新建）→ 解析身份 → 把旧文件对齐成权威身份。
        val resolved = credentials.resolveIdentityId(identityStore.loadExisting())
        val aligned = identityStore.alignTo(resolved)

        assertEquals(authoritativeId, resolved, "凭据是单一真源")
        assertEquals(authoritativeId, aligned.identityId, "对账后 identity.yml 必须承载权威身份")
        assertEquals(
            authoritativeId,
            identityStore.loadExisting().identityId,
            "两份文件必须永远指向同一身份：${listNames(dir)}",
        )
        assertNotEquals(
            dormant.identityId,
            identityStore.loadExisting().identityId,
            "休眠的第二个 UUID 必须在启动时被就地纠正，否则凭据一坏它就会被迁移复活",
        )
    }

    /** 数据目录里的损坏备份（分析用；未做备份时为空）。 */
    private fun corruptBackups(dir: Path): List<Path> =
        Files.newDirectoryStream(dir).use { entries -> entries.toList() }
            .filter { it.fileName.toString().contains(CORRUPT_MARK) }

    /** 断言失败时把目录内容打进消息，免得只看到「数量不对」却不知多/少了什么。 */
    private fun listNames(dir: Path): List<String> =
        Files.newDirectoryStream(dir).use { entries -> entries.toList() }.map { it.fileName.toString() }

    /**
     * 全新安装且用 config.yml 直接连上时，凭据必须**一并记住地址与令牌**。
     *
     * ## 这条防的是什么（真机实测暴露）
     *
     * 早先新建凭据只写 identityId，endpoint / token 留空。看起来「已经接入成功」，
     * 但那份凭据是残缺的：一旦 config.yml 被清空、被换机复制遗漏，agent 就再也连不回去，
     * 「重启后不必再配地址」这个承诺直接落空。真机上表现为重启后完全静默、不发起任何注册。
     *
     * 故 resolveIdentityId 支持传入种子，并在**新建**凭据时落盘。
     */
    @Test
    fun `新建凭据时把配置里的地址与令牌一并落盘`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)

        val identityId =
            store.resolveIdentityId(
                StoredAgentIdentity("", null, isValid = false, error = "身份文件不存在"),
                seedEndpoint = "http://127.0.0.1:50199",
                seedToken = "bn_seed_token",
            )

        val saved = store.load()
        assertNotNull(saved, "新建身份必须落盘凭据")
        assertEquals(identityId, saved.identityId)
        assertEquals("http://127.0.0.1:50199", saved.endpoint, "地址必须写入凭据，否则换机后连不回去")
        assertEquals("bn_seed_token", saved.token, "令牌必须写入凭据")
        assertTrue(saved.canConnect, "种子化之后凭据本身应已具备直连条件")
    }

    /** 已有合法凭据时，种子**不得**覆盖文件里既有的接入信息。 */
    @Test
    fun `已有凭据时种子不覆盖原有地址与令牌`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val existing =
            AgentCredentials(
                identityId = "1f8fad5b-d9cb-469f-a165-70867728950e",
                endpoint = "https://kept.example.com",
                token = "bn_kept",
            )
        store.save(existing)

        val resolved =
            store.resolveIdentityId(
                StoredAgentIdentity("", null, isValid = false, error = "身份文件不存在"),
                seedEndpoint = "http://new.example.com",
                seedToken = "bn_new",
            )

        assertEquals(existing.identityId, resolved, "已有凭据时用凭据里的身份")
        val kept = store.load()
        assertNotNull(kept)
        assertEquals("https://kept.example.com", kept.endpoint, "种子只用于新建，绝不覆盖既有凭据")
        assertEquals("bn_kept", kept.token)
    }

    /** 从旧 `identity.yml` 迁移时同样要带上种子，否则迁移出来的凭据仍是残缺的。 */
    @Test
    fun `迁移旧身份时也带上配置里的接入信息`() {
        val dir = tempDir()
        val store = AgentCredentialsStore(dir, TestJsonCodec)
        val legacyId = "1f8fad5b-d9cb-469f-a165-70867728950e"

        val resolved =
            store.resolveIdentityId(
                StoredAgentIdentity(legacyId, null, isValid = true),
                seedEndpoint = "http://127.0.0.1:50199",
                seedToken = "bn_seed_token",
            )

        assertEquals(legacyId, resolved, "迁移必须搬旧身份，不能换新的")
        val saved = store.load()
        assertNotNull(saved)
        assertEquals("http://127.0.0.1:50199", saved.endpoint, "迁移时同样要记住地址")
        assertEquals("bn_seed_token", saved.token)
    }

    private companion object {
        const val THREADS = 8
        const val ROUNDS = 10

        /** 损坏凭据的备份标记（与实现约定一致：`agent-credentials.json.corrupt-<时间戳>`）。 */
        const val CORRUPT_MARK = ".corrupt-"
    }
}
