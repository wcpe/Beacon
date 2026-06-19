package top.wcpe.beacon.agent.kit;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import top.wcpe.beacon.agent.api.AgentIdentity;
import top.wcpe.beacon.agent.api.BeaconAgent;
import top.wcpe.beacon.agent.api.BeaconAgentProvider;
import top.wcpe.beacon.agent.api.ConfigChangeListener;
import top.wcpe.beacon.agent.api.Discovery;
import top.wcpe.beacon.agent.api.DiscoveryQuery;
import top.wcpe.beacon.agent.api.EffectiveConfig;
import top.wcpe.beacon.agent.api.ListenerHandle;
import top.wcpe.beacon.agent.api.ServiceInstance;
import top.wcpe.beacon.agent.api.TopologyListener;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** BeaconAccess 便捷门面与订阅桥的单元测试：覆盖回退判据、读配置降级、订阅重放与补注册、close 幂等。 */
class BeaconAccessTest {

    /** 每个用例后清空静态门面，避免跨用例污染。 */
    @AfterEach
    void tearDown() {
        BeaconAgentProvider.unregister();
    }

    // ---------------------------------------------------------------------
    // 回退判据：只看 isAvailable()，绝不看 connected()
    // ---------------------------------------------------------------------

    @Test
    void isBeaconPresent未就绪时为false() {
        BeaconAccess access = new BeaconAccess();
        assertFalse(access.isBeaconPresent());
    }

    @Test
    void isBeaconPresent就绪即为true即便未连上控制面() {
        // agent 已注入但 connected()=false（控制面短暂不可用、用本地快照 fail-static）。
        FakeAgent agent = new FakeAgent();
        agent.connected = false;
        BeaconAgentProvider.register(agent);

        BeaconAccess access = new BeaconAccess();
        // 防 split-brain：回退判据只看 isAvailable，控制面挂了仍判「在场」，不应触发本地文件回退。
        assertTrue(access.isBeaconPresent());
    }

    // ---------------------------------------------------------------------
    // 读配置便捷方法：agent 不可用时降级为空，不抛异常
    // ---------------------------------------------------------------------

    @Test
    void agent不可用时读配置返回空集合而非抛异常() {
        BeaconAccess access = new BeaconAccess();
        assertFalse(access.rawConfig("mysql.yml").isPresent());
        assertFalse(access.effectiveMd5().isPresent());
        assertTrue(access.dataIds().isEmpty());
        assertFalse(access.identity().isPresent());
    }

    @Test
    void agent可用时读配置便捷方法透传底层值() {
        FakeAgent agent = new FakeAgent();
        agent.config.put("mysql.yml", "url: jdbc");
        agent.effectiveMd5 = "abc123";
        BeaconAgentProvider.register(agent);

        BeaconAccess access = new BeaconAccess();
        assertEquals("url: jdbc", access.rawConfig("mysql.yml").orElse(null));
        assertEquals("abc123", access.effectiveMd5().orElse(null));
        assertEquals(Collections.singletonList("mysql.yml"), access.dataIds());
        assertEquals("lobby-1", access.identity().map(AgentIdentity::serverId).orElse(null));
    }

    // ---------------------------------------------------------------------
    // 发现便捷方法：agent 不可用时返回空列表，不触发同步 HTTP
    // ---------------------------------------------------------------------

    @Test
    void agent不可用时发现查询返回空列表() {
        BeaconAccess access = new BeaconAccess();
        assertTrue(access.instancesInZone("area1", "zoneA").isEmpty());
        assertTrue(access.instancesInGroup("area1").isEmpty());
        assertTrue(access.query(DiscoveryQuery.builder().build()).isEmpty());
    }

    @Test
    void agent可用时发现便捷方法透传底层结果() {
        FakeAgent agent = new FakeAgent();
        agent.discovery.zoneResult.add(instance("lobby-2"));
        BeaconAgentProvider.register(agent);

        BeaconAccess access = new BeaconAccess();
        List<ServiceInstance> result = access.instancesInZone("area1", "zoneA");
        assertEquals(1, result.size());
        assertEquals("lobby-2", result.get(0).serverId());
    }

    // ---------------------------------------------------------------------
    // 订阅桥：注册即重放当前值
    // ---------------------------------------------------------------------

    @Test
    void 订阅时agent已就绪立即重放当前值并注册底层监听() {
        FakeAgent agent = new FakeAgent();
        agent.config.put("a.yml", "x");
        agent.config.put("b.yml", "y");
        agent.effectiveMd5 = "m1";
        BeaconAgentProvider.register(agent);

        BeaconAccess access = new BeaconAccess();
        RecordingListener listener = new RecordingListener();
        BeaconSubscription sub = access.subscribeConfig(listener);

        // 注册即重放一次：携带当前全部 dataId 与整体 md5。
        assertEquals(1, listener.calls.size());
        assertEquals(new HashSet<>(Arrays.asList("a.yml", "b.yml")), listener.calls.get(0).changed);
        assertEquals("m1", listener.calls.get(0).md5);
        // 已向底层 EffectiveConfig 注册监听。
        assertEquals(1, agent.config().registeredListeners());

        // 底层后续真变更也透传。
        agent.fireChange(new HashSet<>(Collections.singletonList("a.yml")), "m2");
        assertEquals(2, listener.calls.size());
        assertEquals("m2", listener.calls.get(1).md5);

        sub.close();
    }

    // ---------------------------------------------------------------------
    // 订阅桥：subscribe 时 agent 未就绪 → pump 后补注册重放（覆盖竞态）
    // ---------------------------------------------------------------------

    @Test
    void 订阅时agent未就绪pump后补注册并重放() {
        BeaconAccess access = new BeaconAccess();
        RecordingListener listener = new RecordingListener();
        // agent 尚未注入时订阅：此刻不应重放、不应注册底层监听。
        BeaconSubscription sub = access.subscribeConfig(listener);
        assertTrue(listener.calls.isEmpty());

        // agent 仍未就绪，pump 一次也不该有动作。
        sub.pump();
        assertTrue(listener.calls.isEmpty());

        // agent 转为就绪后再 pump：补注册底层监听并重放当前值。
        FakeAgent agent = new FakeAgent();
        agent.config.put("c.yml", "z");
        agent.effectiveMd5 = "m9";
        BeaconAgentProvider.register(agent);

        sub.pump();
        assertEquals(1, listener.calls.size());
        assertEquals("m9", listener.calls.get(0).md5);
        assertEquals(new HashSet<>(Collections.singletonList("c.yml")), listener.calls.get(0).changed);
        assertEquals(1, agent.config().registeredListeners());

        // 已补注册后，重复 pump 不应再重复注册或重复重放。
        sub.pump();
        sub.pump();
        assertEquals(1, listener.calls.size());
        assertEquals(1, agent.config().registeredListeners());

        sub.close();
    }

    @Test
    void agent由不可用转可用再不可用再可用应重新补注册重放() {
        BeaconAccess access = new BeaconAccess();
        RecordingListener listener = new RecordingListener();
        BeaconSubscription sub = access.subscribeConfig(listener);

        // 第一次就绪 → pump 补注册重放。
        FakeAgent first = new FakeAgent();
        first.effectiveMd5 = "m1";
        BeaconAgentProvider.register(first);
        sub.pump();
        assertEquals(1, listener.calls.size());

        // agent 卸载（如 reload）→ 边沿回到不可用。
        BeaconAgentProvider.unregister();
        sub.pump();
        assertEquals(1, listener.calls.size());

        // 新 agent 实例就绪 → 再次补注册重放（覆盖 reload 后新门面场景）。
        FakeAgent second = new FakeAgent();
        second.effectiveMd5 = "m2";
        BeaconAgentProvider.register(second);
        sub.pump();
        assertEquals(2, listener.calls.size());
        assertEquals("m2", listener.calls.get(1).md5);
        assertEquals(1, second.config().registeredListeners());

        sub.close();
    }

    // ---------------------------------------------------------------------
    // close 幂等：注销底层监听、可重复调用
    // ---------------------------------------------------------------------

    @Test
    void close注销底层监听且重复调用安全() {
        FakeAgent agent = new FakeAgent();
        agent.effectiveMd5 = "m1";
        BeaconAgentProvider.register(agent);

        BeaconAccess access = new BeaconAccess();
        RecordingListener listener = new RecordingListener();
        BeaconSubscription sub = access.subscribeConfig(listener);
        assertEquals(1, agent.config().registeredListeners());

        sub.close();
        assertEquals(0, agent.config().registeredListeners());

        // 关闭后底层变更不再回调。
        int before = listener.calls.size();
        agent.fireChange(new HashSet<>(Collections.singletonList("a.yml")), "m2");
        assertEquals(before, listener.calls.size());

        // 重复 close 安全；close 后 pump 不再补注册。
        sub.close();
        sub.pump();
        assertEquals(0, agent.config().registeredListeners());
    }

    @Test
    void 每个BeaconAccess非静态单例可独立构造() {
        BeaconAccess a1 = new BeaconAccess();
        BeaconAccess a2 = new BeaconAccess();
        // 两个实例互不相同（非静态单例）。
        assertFalse(a1 == a2);
        // 但都正确反映同一全局门面状态。
        assertEquals(a1.isBeaconPresent(), a2.isBeaconPresent());
        assertFalse(a1.isBeaconPresent());
    }

    // ---------------------------------------------------------------------
    // 测试替身
    // ---------------------------------------------------------------------

    private static ServiceInstance instance(String serverId) {
        return new ServiceInstance(serverId, "bukkit", "area1", "zoneA",
                "10.0.0.1:25565", "1.0.0", "online", 0, 200, 100);
    }

    /** 记录回调的监听器。 */
    private static final class RecordingListener implements ConfigChangeListener {
        final List<Call> calls = new ArrayList<>();

        @Override
        public void onConfigChanged(Set<String> changedDataIds, String newMd5) {
            calls.add(new Call(new HashSet<>(changedDataIds), newMd5));
        }
    }

    private static final class Call {
        final Set<String> changed;
        final String md5;

        Call(Set<String> changed, String md5) {
            this.changed = changed;
            this.md5 = md5;
        }
    }

    /** 假 BeaconAgent 门面，驱动 kit 的便捷层与订阅桥。 */
    private static final class FakeAgent implements BeaconAgent {
        final Map<String, String> config = new LinkedHashMap<>();
        final FakeDiscovery discovery = new FakeDiscovery();
        final FakeEffectiveConfig effectiveConfig = new FakeEffectiveConfig(this);
        String effectiveMd5;
        boolean connected = true;

        @Override
        public AgentIdentity identity() {
            return new AgentIdentity("prod", "lobby-1", "bukkit", "area1", "zoneA");
        }

        @Override
        public FakeEffectiveConfig config() {
            return effectiveConfig;
        }

        @Override
        public Discovery discovery() {
            return discovery;
        }

        @Override
        public boolean connected() {
            return connected;
        }

        @Override
        public Optional<String> effectiveMd5() {
            return Optional.ofNullable(effectiveMd5);
        }

        @Override
        public boolean awaitRegistered(long timeoutMillis) {
            return connected;
        }

        /** 触发一次底层有效配置变更。 */
        void fireChange(Set<String> changed, String md5) {
            effectiveMd5 = md5;
            effectiveConfig.fire(changed, md5);
        }
    }

    /** 假 EffectiveConfig：记录注册的监听器，支持手动触发。 */
    private static final class FakeEffectiveConfig implements EffectiveConfig {
        private final FakeAgent owner;
        private final List<ConfigChangeListener> listeners = new ArrayList<>();

        FakeEffectiveConfig(FakeAgent owner) {
            this.owner = owner;
        }

        int registeredListeners() {
            return listeners.size();
        }

        void fire(Set<String> changed, String md5) {
            for (ConfigChangeListener l : new ArrayList<>(listeners)) {
                l.onConfigChanged(changed, md5);
            }
        }

        @Override
        public List<String> dataIds() {
            return new ArrayList<>(owner.config.keySet());
        }

        @Override
        public Optional<String> raw(String dataId) {
            return Optional.ofNullable(owner.config.get(dataId));
        }

        @Override
        public Optional<String> format(String dataId) {
            return owner.config.containsKey(dataId) ? Optional.of("yaml") : Optional.empty();
        }

        @Override
        public Optional<String> md5(String dataId) {
            return owner.config.containsKey(dataId) ? Optional.of("hash-" + dataId) : Optional.empty();
        }

        @Override
        public ListenerHandle onChange(ConfigChangeListener listener) {
            listeners.add(listener);
            return () -> listeners.remove(listener);
        }
    }

    /** 假 Discovery：固定返回预置结果。 */
    private static final class FakeDiscovery implements Discovery {
        final List<ServiceInstance> zoneResult = new ArrayList<>();
        final List<ServiceInstance> groupResult = new ArrayList<>();
        final List<ServiceInstance> queryResult = new ArrayList<>();

        @Override
        public List<ServiceInstance> query(DiscoveryQuery query) {
            return new ArrayList<>(queryResult);
        }

        @Override
        public List<ServiceInstance> instancesInZone(String group, String zone) {
            return new ArrayList<>(zoneResult);
        }

        @Override
        public List<ServiceInstance> instancesInGroup(String group) {
            return new ArrayList<>(groupResult);
        }

        @Override
        public ListenerHandle watch(TopologyListener listener) {
            // 假实现：不触发回调，返回 no-op 句柄。
            return () -> {
            };
        }
    }
}
