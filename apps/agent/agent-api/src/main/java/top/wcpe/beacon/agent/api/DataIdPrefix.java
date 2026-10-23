package top.wcpe.beacon.agent.api;

import java.util.Collection;
import java.util.LinkedHashSet;
import java.util.Set;

/**
 * dataId 路径前缀匹配工具：**前缀过滤规则的唯一真源**。
 *
 * <p>规则只有一条——从字符串开头逐字比较（{@code startsWith}），不做通配、不做分段边界推断。
 * 规则集中在此，是因为它要被两处调用：agent-core 的变更派发（决定「这次要不要叫醒这个监听器」）
 * 与 agent-kit 的注册重放（决定「基线里给哪些 dataId」）。两处各写一份，迟早会分叉成
 * 「真变更按 A 规则命中、重放按 B 规则命中」这种只在某个插件上才发作的怪事。</p>
 *
 * <p><b>为什么放在 agent-api</b>：kit 只依赖 agent-api（不依赖 core），而 kit 的重放同样要裁剪，
 * 故规则必须落在两者都可见的最小模块里。</p>
 *
 * <p><b>为什么前缀建议以 {@code /} 结尾</b>：这是**字符串前缀**而不是目录边界匹配，
 * 前缀 {@code plugins/Foo} 同样会命中 {@code plugins/Foo2/config.yml}。
 * 插件应使用目录前缀 {@code plugins/Foo/}，让匹配自然止于目录边界。</p>
 */
public final class DataIdPrefix {

    private DataIdPrefix() {
        // 纯静态工具类，禁止实例化。
    }

    /**
     * 该前缀是否需要过滤。
     *
     * @param pathPrefix 前缀；{@code null} 或空串表示**不过滤**（等价于老的无前缀订阅语义）
     */
    public static boolean isFiltering(String pathPrefix) {
        return pathPrefix != null && !pathPrefix.isEmpty();
    }

    /**
     * 取以该前缀开头的 dataId 子集，保持入参顺序，并丢弃 {@code null} 项。
     *
     * <p>前缀为 {@code null} / 空串时返回入参的**全量副本**（不过滤）。调用方据此可以放心
     * 区分「过滤后零匹配」与「本来就没有配置项」——前者该跳过回调，后者在无前缀订阅里
     * 仍需照常回调一次（老语义：注册即重放，哪怕当前为空）。</p>
     *
     * <p>始终返回**新建可修改集合**，调用方随意增删不会影响入参。</p>
     */
    public static Set<String> filter(String pathPrefix, Collection<String> dataIds) {
        Set<String> matched = new LinkedHashSet<>();
        if (!isFiltering(pathPrefix)) {
            for (String dataId : dataIds) {
                if (dataId != null) {
                    matched.add(dataId);
                }
            }
            return matched;
        }
        for (String dataId : dataIds) {
            if (dataId != null && dataId.startsWith(pathPrefix)) {
                matched.add(dataId);
            }
        }
        return matched;
    }
}
