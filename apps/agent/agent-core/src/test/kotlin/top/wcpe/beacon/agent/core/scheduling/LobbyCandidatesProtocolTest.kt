package top.wcpe.beacon.agent.core.scheduling

import top.wcpe.beacon.agent.core.client.LobbyCandidates
import top.wcpe.beacon.agent.core.client.SchedCandidates
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

class LobbyCandidatesProtocolTest {
    @Test
    fun `候选帧保留可选大厅段`() {
        val lobby = LobbyCandidates(12L, true, listOf(candidateEntry("lobby-1", 92, "healthy", true, 8, 300)))

        val snapshot = SchedCandidates(1_000L, emptyList(), lobby).toSnapshot(1_200L)

        assertEquals(lobby, snapshot.lobby)
    }

    @Test
    fun `旧控制面缺失大厅段保持为空而不从小区推断`() {
        val snapshot = SchedCandidates(1_000L, emptyList()).toSnapshot(1_200L)

        assertNull(snapshot.lobby)
    }
}
