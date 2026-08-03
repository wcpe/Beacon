package top.wcpe.beacon.agent.core.identity

import top.wcpe.beacon.agent.core.client.ActiveBinding
import top.wcpe.beacon.agent.core.client.JsonTree
import top.wcpe.beacon.agent.core.filetree.AtomicFileWriter
import top.wcpe.beacon.agent.core.transport.JsonCodec
import java.io.File
import java.nio.charset.StandardCharsets

/** 控制面已确认绑定的本地只读缓存；不含 token，且只由 active runtime 写入。 */
class IdentityBindingSnapshotStore(
    private val file: File,
    private val codec: JsonCodec,
) {
    fun write(
        identity: AgentIdentity,
        binding: ActiveBinding,
    ) {
        if (!identity.hasV2Identity() || binding.namespace.isBlank() || binding.serverId.isBlank()) return
        val content =
            linkedMapOf<String, Any?>(
                "formatVersion" to 1,
                "identityId" to identity.identityId,
                "namespace" to binding.namespace,
                "serverId" to binding.serverId,
                "kind" to identity.role,
                "boundAt" to binding.boundAt,
                "bindingFingerprint" to binding.bindingFingerprint,
                "address" to binding.compatAddress,
            )
        AtomicFileWriter.write(file, codec.encode(content).toByteArray(StandardCharsets.UTF_8))
    }

    fun load(identity: AgentIdentity): ActiveBinding? {
        if (!file.exists()) return null
        return try {
            val data = JsonTree.asObject(codec.decode(file.readText(StandardCharsets.UTF_8)))
            val namespace = JsonTree.strOr(data, "namespace", "")
            val serverId = JsonTree.strOr(data, "serverId", "")
            val boundAt = JsonTree.strOr(data, "boundAt", "")
            val fingerprint = JsonTree.strOr(data, "bindingFingerprint", "")
            val compatAddress = JsonTree.strOr(data, "address", "")
            // 逐项校验快照一致性：格式版本 / identityId / role / 命名空间 / serverId 任一不符即视为不可用。
            if (!isMetadataConsistent(data, identity, namespace, serverId)) {
                null
            } else {
                if (boundAt.isBlank() || compatAddress.isBlank() || !FINGERPRINT.matches(fingerprint)) {
                    null
                } else {
                    ActiveBinding(namespace, serverId, boundAt, fingerprint, compatAddress)
                }
            }
        } catch (_: Exception) {
            null
        }
    }

    /** 仅删除 agent 自管快照；控制面否决或 namespace 不匹配后禁止回退旧绑定。 */
    fun invalidate() {
        if (file.exists() && !file.delete()) {
            // 快照残留时仍会因身份/命名空间校验 fail-closed，不把删除失败当作可用快照。
        }
    }

    /** 逐项校验快照元数据一致性：格式版本 / identityId / role / 命名空间 / serverId 任一不符即视为不可用。 */
    private fun isMetadataConsistent(
        data: Map<String, Any?>,
        identity: AgentIdentity,
        namespace: String,
        serverId: String,
    ): Boolean {
        val versionOk = JsonTree.intOr(data, "formatVersion", 0) == FORMAT_VERSION
        val identityOk = JsonTree.strOr(data, "identityId", "") == identity.identityId
        val roleOk = JsonTree.strOr(data, "kind", "") == identity.role
        val bindingFieldsOk = namespace.isNotBlank() && serverId.isNotBlank()
        return versionOk && identityOk && roleOk && bindingFieldsOk
    }

    private companion object {
        const val FORMAT_VERSION = 1
        val FINGERPRINT = Regex("[0-9a-f]{64}")
    }
}
