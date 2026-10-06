package top.wcpe.beacon.agent.api;

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/**
 * 选服决策的「<b>准入作用域</b>」：一组<b>备选</b>，每个备选是一组「候选节点必须自己声明过的键值标签」
 * （FR-244，本仓对 FR-243 §2.2「声明不进入调度决策」那一条的重新裁定）。
 *
 * <h2>它解决什么</h2>
 *
 * <p>需要在<b>决策之前</b>排除掉「不服务我这个区 / 不具备这个资格」的节点时，用它把条件带进
 * {@link BeaconScheduling#acquireCandidate(String, String, AdmissionScope)}——决策在<b>生成候选的那一刻</b>
 * 就按本作用域收窄，「先选中、再被调用方拒掉」这条路径因此不必再走一遍。</p>
 *
 * <h2>语义（四条，钉死）</h2>
 *
 * <ol>
 *   <li><b>备选之间是 OR</b>：{@link #alternatives()} 里<b>任一</b>个备选被满足，该节点就算满足本作用域。
 *       它存在的理由是「<b>不限制</b> ∨ <b>明确受理某个取值</b>」这类条件——调用方的真值表里
 *       "没有收窄服务范围"与"收窄到正好包含这个取值"是<b>并列</b>的两种受理形态，
 *       用单一 AND 表达不了，而<b>不</b>该让本仓去解释某个键的值结构（如逗号分列的清单）。</li>
 *   <li><b>备选之内是 AND</b>：一个备选里的键值对<b>全部</b>满足，该备选才成立。</li>
 *   <li><b>精确相等</b>：值按字符串<b>逐字符相等</b>比较，<b>不做</b> trim、大小写折叠或任何归一化——
 *       归一化只在<b>写入声明的那一方</b>做一次（谁写标签谁把值写规范），
 *       两边都做二次归一化会让「声明里写的是什么」与「判定用的值是什么」出现两个版本。</li>
 *   <li><b>不解释 key 与值结构</b>：本类只承载「哪些键值对是必须的、它们怎么并列」，<b>不定义</b>
 *       任何 key 的业务含义，也<b>不</b>解释任何值的内部结构（与 FR-243 / ADR-0086 §5 同口径）。
 *       命名空间与取值约定由调用方自持，例如接入方的 {@code com.example.*}。</li>
 * </ol>
 *
 * <h2>与「没有标签」的关系</h2>
 *
 * <p>作用域非空、而某个节点<b>没有声明</b>（或声明里没有该键）时，该节点<b>不满足</b>本作用域
 * （除非另有备选成立）。这是刻意的：把「读不到声明」读成「满足」会让准入静默失效，方向不对称，
 * 故取严格一侧。反过来，<b>空作用域</b>不要求任何节点有标签——只声明了容量的老节点照样进候选集合。</p>
 *
 * <p><b>"空"只有一种形态</b>：{@link #empty()}、{@link #of(Map) of(null)}、
 * {@code of(空 map)}、{@code anyOf(空列表)}、含空备选的 OR、以及 {@code anyOf(empty(), ...)}
 * 都在入口规约到同一个形态——<b>无约束力、不排除任何候选</b>，{@link #isEmpty()} 为 true
 * 且与 {@link #empty()} 内容相等（不变式见 {@link #isEmpty()} 的 javadoc）。
 * 因此不存在"非空却恒真"的中间态，调用方读 {@link #isEmpty()} 与读判定结果永远一致。</p>
 *
 * <p><b>值对象不可变</b>：备选与其中的键值对在构造时防御性拷贝并包装为不可修改视图，
 * 构造后外部改动不影响本对象。</p>
 */
public final class AdmissionScope {

    /** 空作用域单例（无状态，可安全共享）。 */
    private static final AdmissionScope EMPTY =
            new AdmissionScope(Collections.<Map<String, String>>emptyList());

    /** OR 备选；恒非 null，空列表表示「不按标签收窄」。 */
    private final List<Map<String, String>> alternatives;

    private AdmissionScope(List<Map<String, String>> alternatives) {
        this.alternatives = alternatives;
    }

    /**
     * 空作用域：不按标签收窄（与不带作用域的那条重载等价）。
     *
     * <p>它<b>不是</b>「没有候选」的意思，也<b>不</b>要求节点声明过任何标签。</p>
     */
    public static AdmissionScope empty() {
        return EMPTY;
    }

    /**
     * 单个要求（最常用形态：接入方给「我这台必须服务我这个区」这类单条条件）——一个备选、一条键值。
     *
     * @param key   标签键（由调用方命名，本仓不解释其语义）；非空白
     * @param value 标签值；要求与节点声明的值<b>精确相等</b>；非 null
     */
    public static AdmissionScope of(String key, String value) {
        return of(single(key, value));
    }

    /**
     * 一组要求（<b>一个</b>备选，AND 语义）。
     *
     * @param requiredLabels 必须全部满足的键值对；{@code null} 或空 map = 不按标签收窄。
     *                       键非空白、值非 null。同一键只能出现一次（{@link Map} 本就不允许重复键），
     *                       故「同一个键要接受多个值」不能在一个备选里表达——那种需求要么是
     *                       {@link #anyOf(java.util.List)} 的多个备选，要么属于<b>接入方自己的命名约定</b>
     */
    public static AdmissionScope of(Map<String, String> requiredLabels) {
        if (requiredLabels == null || requiredLabels.isEmpty()) {
            return EMPTY;
        }
        return new AdmissionScope(Collections.singletonList(copyAlternative(requiredLabels)));
    }

    /**
     * 多个备选（<b>OR</b> 语义）：任一备选被满足即准入。
     *
     * <p><b>入口规约</b>：列表里一旦出现<b>空 map 备选</b>（该备选恒成立），整个作用域就在入口处
     * 退回 {@link #empty()}——「<b>恒真</b> ∨ <b>任意</b>」恒真，此时它已不排除任何候选。
     * 不做这条规约，对象会同时呈现"不收窄"（判定恒真）与"非空"（{@link #isEmpty()} 为 false）
     * 两种互相矛盾的形态：调用方据此把一次本来正常的决策读成"不可用"，还会把这个空备选
     * 原样下发到线上协议。规约在<b>拷贝并校验之后</b>做，故非法入参（键为空白等）在任何位置
     * 都会被如实拒绝。</p>
     *
     * @param alternatives 备选列表；{@code null} 或空列表 = 不按标签收窄。
     *                     每个备选内部的键值对是 AND；单个备选为空 map 恒成立，故整个列表
     *                     规约为 {@link #empty()}
     */
    public static AdmissionScope anyOf(List<Map<String, String>> alternatives) {
        if (alternatives == null || alternatives.isEmpty()) {
            return EMPTY;
        }
        List<Map<String, String>> copied = new ArrayList<Map<String, String>>(alternatives.size());
        boolean anyAlwaysTrue = false;
        for (Map<String, String> alternative : alternatives) {
            Objects.requireNonNull(alternative, "备选不能为空（要表达「不按标签收窄」请用 AdmissionScope.empty()）");
            Map<String, String> copiedAlternative = copyAlternative(alternative);
            // 空备选 = 「这个节点无条件被受理」，OR 之下它让其余备选全部失去意义。
            anyAlwaysTrue |= copiedAlternative.isEmpty();
            copied.add(copiedAlternative);
        }
        if (anyAlwaysTrue) {
            return EMPTY;
        }
        return new AdmissionScope(Collections.unmodifiableList(copied));
    }

    /**
     * 把若干作用域并列成一个 OR 作用域（便于把 {@link #of(String, String)} 的产物拼起来）。
     *
     * <p><b>OR 之下方向必须一致</b>：任一入参 {@link #isEmpty()} 为 true（"不按标签收窄"）时，
     * 结果就是 {@link #empty()}——「<b>不限制</b> ∨ <b>某个取值</b>」本来就是"不限制"。
     * 若反过来把空作用域当成"零贡献"、只把其余备选取并集，调用方"配置缺失就传 {@code empty()}"
     * 的写法会<b>静默收紧</b>准入（本该全放行，却变成只放行剩下那几个备选），
     * 与 {@link #empty()} 的语义正好相反。</p>
     *
     * @param scopes 并列的作用域；{@code null} 元素视为"没有这个备选"被跳过（{@code null} 数组或空数组
     *               与空列表同义：不按标签收窄）
     */
    public static AdmissionScope anyOf(AdmissionScope... scopes) {
        if (scopes == null || scopes.length == 0) {
            return EMPTY;
        }
        List<Map<String, String>> merged = new ArrayList<Map<String, String>>();
        for (AdmissionScope scope : scopes) {
            if (scope == null) {
                continue;
            }
            if (scope.isEmpty()) {
                // 「恒真 ∨ 任意」恒真：OR 里任何一项"不按标签收窄"都让整个作用域失去约束力。
                return EMPTY;
            }
            merged.addAll(scope.alternatives);
        }
        return anyOf(merged);
    }

    /**
     * 本作用域是否<b>不按标签收窄</b>（true 时决策行为与不带作用域的那条重载逐位一致）。
     *
     * <p><b>不变式（本类只有这一种"空"）</b>：{@code isEmpty() == true} 与「<b>无约束力</b>」
     * 严格等价——{@link #admits(Map)} 对任何入参（含 {@code null}）恒为 true，
     * 即<b>不排除任何候选</b>；并且此时本对象与 {@link #empty()} <b>内容相等</b>
     * （{@link #equals(Object)} 为 true、{@link #hashCode()} 相同）。所有"空"的写法
     * （{@code empty()}、{@code of(null)}、{@code of(空 map)}、{@code anyOf(空列表)}、
     * {@code anyOf(listOf(空 map))}、{@code anyOf(empty(), ...)}）都在入口规约到同一形态，
     * 不存在"非空却恒真"的中间态。</p>
     *
     * <p>它<b>不是</b>"没有候选"的意思：判"有没有候选"读
     * {@link ScheduleResult#state()}，不要把本方法的结果当结论用。</p>
     */
    public boolean isEmpty() {
        return alternatives.isEmpty();
    }

    /**
     * OR 备选：每个元素是一组必须<b>全部</b>满足的键值标签。
     *
     * @return 不可修改的列表，元素是不可修改的 map；空列表表示「不按标签收窄」
     */
    public List<Map<String, String>> alternatives() {
        return alternatives;
    }

    /**
     * 判定一组节点自声明标签是否满足本作用域（纯逻辑，无 I/O）。
     *
     * <p><b>不变式</b>：{@link #isEmpty()} 为 true 时无约束力——本方法对<b>任何</b>入参
     * （含 {@code null}、空 map）恒为 {@code true}，即不排除任何候选。</p>
     *
     * @param declaredLabels 节点<b>自己声明</b>的标签；{@code null} 视为「没声明过」
     * @return {@link #isEmpty()} 为 true 时恒为 {@code true}（空作用域不要求节点声明过任何标签）；
     *         否则要求<b>至少一个</b>备选成立（备选内全部满足，精确相等，不做归一化，见类首段的语义 ③）
     */
    public boolean admits(Map<String, String> declaredLabels) {
        if (alternatives.isEmpty()) {
            // 正常路径已在入口规约（见 isEmpty 的不变式）；这里是防反射构造的恒真防御分支。
            return true;
        }
        for (Map<String, String> alternative : alternatives) {
            if (satisfies(declaredLabels, alternative)) {
                return true;
            }
        }
        return false;
    }

    /** 单个备选是否成立：备选内每一对都要在声明里精确命中。 */
    private static boolean satisfies(Map<String, String> declaredLabels, Map<String, String> alternative) {
        if (alternative.isEmpty()) {
            return true;
        }
        if (declaredLabels == null) {
            // 节点没有任何自声明：非空备选一律不满足（把「读不到」读成「满足」会让准入静默失效）。
            return false;
        }
        for (Map.Entry<String, String> required : alternative.entrySet()) {
            String declared = declaredLabels.get(required.getKey());
            if (declared == null || !declared.equals(required.getValue())) {
                return false;
            }
        }
        return true;
    }

    /** 单条键值 → 单备选（顺带把入参校验集中在一处）。 */
    private static Map<String, String> single(String key, String value) {
        Objects.requireNonNull(key, "标签键不能为空");
        Objects.requireNonNull(value, "标签值不能为空");
        Map<String, String> one = new LinkedHashMap<String, String>(1);
        one.put(key, value);
        return one;
    }

    /** 校验并冻结一个备选（键非空白、值非 null，拷贝后不可修改）。 */
    private static Map<String, String> copyAlternative(Map<String, String> alternative) {
        if (alternative.isEmpty()) {
            return Collections.emptyMap();
        }
        Map<String, String> copy = new LinkedHashMap<String, String>(alternative.size());
        for (Map.Entry<String, String> entry : alternative.entrySet()) {
            String key = entry.getKey();
            String value = entry.getValue();
            Objects.requireNonNull(key, "标签键不能为空");
            Objects.requireNonNull(value, "标签值不能为空");
            if (key.trim().isEmpty()) {
                throw new IllegalArgumentException("标签键不能为空白");
            }
            copy.put(key, value);
        }
        return Collections.unmodifiableMap(copy);
    }

    /**
     * 按 {@link #alternatives()} 的内容相等比较。
     *
     * <p><b>不变式</b>：{@link #isEmpty()} 为 true 的对象与 {@link #empty()} <b>相等</b>，
     * 因为空的写法只有一种（全部在入口规约到 {@code EMPTY} 单例的形态），
     * 故"空"与"无约束力"在相等性上也是同一个东西——调用方可以用
     * {@code scope.equals(AdmissionScope.empty())} 或 {@code isEmpty()} 互推。</p>
     */
    @Override
    public boolean equals(Object other) {
        if (this == other) {
            return true;
        }
        if (!(other instanceof AdmissionScope)) {
            return false;
        }
        return alternatives.equals(((AdmissionScope) other).alternatives);
    }

    @Override
    public int hashCode() {
        return alternatives.hashCode();
    }

    /** 诊断用：只印备选个数与各备选的键，不展开值（值可能含运维侧的业务标识）。 */
    @Override
    public String toString() {
        StringBuilder text = new StringBuilder("AdmissionScope{anyOf=[");
        for (int i = 0; i < alternatives.size(); i++) {
            if (i > 0) {
                text.append(", ");
            }
            text.append(alternatives.get(i).keySet());
        }
        return text.append("]}").toString();
    }
}
