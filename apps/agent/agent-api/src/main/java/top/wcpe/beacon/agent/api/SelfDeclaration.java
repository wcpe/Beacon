package top.wcpe.beacon.agent.api;

/**
 * 节点自声明门面（FR-243，见 ADR-0086）：刷新<b>本节点自己</b>的容量与自定义标签。
 *
 * <p><b>窄写入面</b>：只能声明自己的容量与标签，<b>改配置 / 改 zone / 写他人一律不可达</b>
 * （请求体里的 namespace / serverId 只用于定位，身份以 agent 自身在册归属为准）。</p>
 *
 * <p><b>线程</b>：同步 HTTP，<b>调用方须在异步线程发起</b>，绝不在 MC 主线程调用
 * （与 {@link Discovery#query} 的既有约定一致）。</p>
 *
 * <p><b>幂等</b>：重复声明 = 刷新，不新增实例、不叠加标签；{@link NodeDeclaration} 里缺省的字段
 * 保持原值（部分刷新）。</p>
 *
 * <p><b>结论</b>：见 {@link DeclarationOutcome}——三类结论取值可区分，绝不外抛异常
 * （不可用时返回 {@link DeclarationOutcome.Status#UNAVAILABLE}，业务侧据此优雅降级）。</p>
 */
public interface SelfDeclaration {

    /**
     * 声明（刷新）本节点的容量与 / 或标签。
     *
     * @param declaration 本次要刷新的字段（各自可选）；{@code null} 由实现按「无有效字段」拒绝
     * @return 三档结论：APPLIED（回带生效值）/ REJECTED（带原因）/ UNAVAILABLE（可重试）；绝不返回 null
     */
    DeclarationOutcome declare(NodeDeclaration declaration);
}
