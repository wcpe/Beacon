package service

import (
	"strconv"
	"strings"
)

// agent 配置值的类型标记（FR-277）。
//
// 与 model.SettingValueType* 同名字同取值不是偶然——两者都只是「值以字符串落库、类型由应用层
// 校验」这一约定的载体。但它们描述的**不是同一件事**：这里是 agent 侧 config.yml 的键类型
// （含 string-list，控制面自身设置没有这种类型），那边是控制面自身热改项的类型。故不共用
// 常量集，避免将来一方扩类型时把另一方的语义带偏。
const (
	agentSettingTypeInt        = "int"
	agentSettingTypeBool       = "bool"
	agentSettingTypeString     = "string"
	agentSettingTypeStringList = "string-list"
)

// agentSettingMeta 是单个可下发键的元数据：类型 / 中文说明 / 危险标记 / 取值范围 / 枚举。
//
// desc 直接抄 agent 侧 AgentBootstrap 的语义（默认值与单位），因为运维在 Web 上填值时，
// 唯一能防止「填了 30 却以为是秒」的信息就是这里的单位说明。
type agentSettingMeta struct {
	// 值类型（决定 validateAgentSettingValue 走哪条校验）
	valueType string
	// 面向运维的中文说明（供前端展示；含单位与默认值）
	desc string
	// 是否高危项：改动会扩大 agent 对本机的处置能力，前端须显著告警
	dangerous bool
	// int 类型的闭区间下界与上界（非 int 忽略）
	min int
	max int
	// string 类型的枚举校验（nil 表示不限）
	enumOK func(string) bool
}

// agentSettingKeys 是可下发键的元数据表（FR-277）。
//
// ## 为什么是**白名单**而不是「未知键放行」
//
// agent 侧 PushedConfigPolicy 对未知键是**放行**的（新增可调参数不需要改那层），这是刻意
// 留给 agent 的前向兼容空间。控制面这边必须更严：本表是运维能填的唯一入口，若未知键也接受，
// 一个拼写错误（`delivery.config-reload-timeout_ms`）会静默通过——库里有值、Web 显示已下发，
// 而 agent 根本不认识这个键，表现为「改了没生效且毫无提示」。本仓对静默失效一贯零容忍，
// 故控制面把「值有没有被消费方认识」这件事在写入时就说清楚。
//
// 代价是新增 agent 配置项要同时改这里：这是**有意**的，改这里的人必然看过 agent 侧语义。
var agentSettingKeys = map[string]agentSettingMeta{
	// —— 通信时序（agent ↔ 控制面）——
	"timing.poll-timeout-ms": {
		valueType: agentSettingTypeInt, min: 1000, max: 600000,
		desc: "长轮询单次挂起上限（毫秒），默认 30000",
	},
	"timing.request-timeout-ms": {
		valueType: agentSettingTypeInt, min: 100, max: 120000,
		desc: "单次 HTTP 请求超时（毫秒），默认 5000",
	},
	"timing.heartbeat-fallback-ms": {
		valueType: agentSettingTypeInt, min: 1000, max: 600000,
		desc: "长轮询不可用时的心跳回退间隔（毫秒），默认 10000",
	},

	// —— 重连退避 ——
	"backoff.initial-ms": {
		valueType: agentSettingTypeInt, min: 100, max: 600000,
		desc: "重连初始退避（毫秒），默认 1000",
	},
	"backoff.max-ms": {
		valueType: agentSettingTypeInt, min: 1000, max: 3600000,
		desc: "重连退避上限（毫秒），默认 30000",
	},
	"backoff.multiplier": {
		valueType: agentSettingTypeString, enumOK: numericRange(1, 10),
		desc: "重连退避倍数（十进制小数，1~10），默认 2.0",
	},
	"backoff.jitter-ratio": {
		valueType: agentSettingTypeString, enumOK: numericRange(0, 1),
		desc: "重连退避抖动比例（0~1），默认 0.2",
	},

	// —— 本地有效配置快照 ——
	"snapshot.enabled": {
		valueType: agentSettingTypeBool,
		desc:      "是否落本地有效配置快照（fail-static 用），默认 true",
	},
	"snapshot.file-name": {
		valueType: agentSettingTypeString, enumOK: safeRelativeFileName,
		desc: "有效配置快照文件名（不含路径分隔符），默认 effective-config.snapshot.json",
	},

	// —— 文件树托管 ——
	"file-tree.enabled": {
		valueType: agentSettingTypeBool,
		desc:      "是否启用文件树托管同步，默认 true",
	},
	"file-tree.target-sub-dir": {
		valueType: agentSettingTypeString, enumOK: safeRelativePath,
		desc: "文件树目标子目录（相对服务器根，空=服务器根）",
	},
	"file-tree.applied-manifest-file-name": {
		valueType: agentSettingTypeString, enumOK: safeRelativeFileName,
		desc: "文件树已应用清单文件名，默认 file-tree.applied.json",
	},

	// —— 三方插件文件覆盖（高风险）——
	"override.command-whitelist": {
		valueType: agentSettingTypeStringList, dangerous: true,
		desc: "允许 agent 执行的重载命令白名单（逗号或换行分隔的命令首 token）。**高危**：填得越宽，" +
			"控制面能对本机执行的命令越多；空值等于关闭命令派发能力",
	},
	"override.backup-dir-name": {
		valueType: agentSettingTypeString, enumOK: safeRelativeFileName,
		desc: "覆盖前备份目录名（相对 agent 数据目录），默认 override-backup",
	},

	// —— 跨服消息模块 ——
	"messaging.enabled": {
		valueType: agentSettingTypeBool,
		desc:      "是否启用 agent 内置跨服消息模块，默认 false（关闭）",
	},
	"messaging.rpc-timeout-ms": {
		valueType: agentSettingTypeInt, min: 100, max: 600000,
		desc: "跨服消息 RPC 默认超时（毫秒），默认 5000",
	},

	// —— BC 代理归属（仅 proxy 壳层消费）——
	"proxy.home-group": {
		valueType: agentSettingTypeString,
		desc:      "BC 代理服务的大区（默认空=未配，默认入口取首个在线子服）",
	},
	"proxy.home-zone": {
		valueType: agentSettingTypeString,
		desc:      "BC 代理服务的小区（默认空=未配）",
	},

	// —— 文件资产索引 ——
	"assets.enabled": {
		valueType: agentSettingTypeBool,
		desc:      "是否启用文件资产索引扫描，默认 true",
	},
	"assets.scan-interval-sec": {
		valueType: agentSettingTypeInt, min: 300, max: 604800,
		desc: "资产周期扫描间隔（秒），默认 1800；下限 300（agent 侧亦会收口到 300，防过频踩踏）",
	},
	"assets.manifest-file-name": {
		valueType: agentSettingTypeString, enumOK: safeRelativeFileName,
		desc: "本地资产清单缓存文件名，默认 asset-manifest.json",
	},

	// —— 配置重载（交付编排消费，FR-274 / FR-278）——
	"delivery.config-reload-timeout-ms": {
		// max 与 agent 侧 MAX_RELOAD_TIMEOUT_MS（3_600_000）保持同值。
		//
		// 此前这里写 600000、agent 侧收口到 3600000：方向虽安全（服务端更严），但运维在
		// config.yml 里写 1800000 能生效、在 Web 上填同样的值却被 400 拒，两边"合法范围"
		// 说的不是一回事，而 PRD 明确要求上限 3600000ms。改齐并在此处互相引用。
		valueType: agentSettingTypeInt, min: 0, max: 3600000,
		desc: "单业务插件配置重载超时（毫秒），默认 30000，上限 3600000；填 0 表示不设超时（不建议，卡死插件会一直占住）。" +
			"须小于控制面 activateTimeoutSec，否则控制面先判整体超时、回执里看不出是哪个插件卡住",
	},
	"delivery.reload-pool-size": {
		valueType: agentSettingTypeInt, min: 1, max: 256,
		desc: "单服并发 reload 的插件数上限，默认 64（单服装 60+ 插件是常见形态）。" +
			"下发后 agent 侧旧线程池优雅退役、在途 reload 跑完，新池接管后续派发（不中断、不丢任务）",
	},
}

// agentSettingForbiddenKeys 是**禁止由控制面下发**的键（FR-277）。
//
// 与 agent 侧 PushedConfigPolicy.forbiddenKeys 逐字对齐，是**双保险**：
//   - beacon.endpoints：改了等于把 agent 指向另一个控制面 → 目标劫持
//   - beacon.bootstrap-token：改了等于换身份接入或让 agent 再也接不回来 → 身份劫持
//   - identity.*：本机身份的权威在注册/审批流程，走配置下发这条旁路必然与它打架
//
// agent 侧已经会在落盘前过滤并告警；服务端再挡一次的理由是：**控制面自身**才是这里的
// 信任边界。若控制面被攻破或写入了这类键，agent 侧的过滤是最后一道，而服务端拒绝能让
// 这次越权尝试在库与审计里留下痕迹（而不是「写进去了、只是没人用」）。
var agentSettingForbiddenKeys = map[string]struct{}{
	"beacon.endpoints":       {},
	"beacon.bootstrap-token": {},
	"identity.server-id":     {},
	"identity.identity-id":   {},
}

// agentSettingForbiddenPrefixes 是禁止下发的键前缀（identity. 整段属本地权威）。
var agentSettingForbiddenPrefixes = []string{"identity."}

// agentSettingKeyAllowed 判断某键是否允许下发（false 且 forbidden=true 表示命中禁区）。
//
// 返回值分离「禁区」与「未知」，是因为两者对运维的处置方向完全不同：
// 禁区是「这个键永远不该由 Web 管」，未知是「这个键控制面不认识，可能是拼错或需要升级控制面」。
func agentSettingKeyAllowed(key string) (allowed, forbidden bool) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return false, false
	}
	if _, hit := agentSettingForbiddenKeys[trimmed]; hit {
		return false, true
	}
	for _, prefix := range agentSettingForbiddenPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return false, true
		}
	}
	_, known := agentSettingKeys[trimmed]
	return known, false
}

// agentSettingMetaFor 取某键的元数据；未知键返回 (zero, false)。
func agentSettingMetaFor(key string) (agentSettingMeta, bool) {
	meta, ok := agentSettingKeys[strings.TrimSpace(key)]
	return meta, ok
}

// validateAgentSettingValue 按元数据校验值：整数解析 + 闭区间、布尔解析、枚举、列表非空项。
//
// 这里**不**复用 validateSettingValue：那张表按 value_type 三分支且枚举语义绑定控制面自身
// （如 log.level），而 agent 配置有 string-list、有「0 合法」的超时（delivery.config-reload-timeout-ms），
// 混用会把两套约束搅在一起。
// agentSettingValueMaxRunes 是单个配置值的字符上限。
//
// 与 model.AgentSetting.Value 的 `size:4096` 同值：超出这一列宽在 MySQL 严格模式下必然 1406。
// 两者必须一起改——只改列宽不放开这里会让新容量用不上，只放开这里不改列宽会退化成 500。
const agentSettingValueMaxRunes = 4096

func validateAgentSettingValue(meta agentSettingMeta, value string) bool {
	trimmed := strings.TrimSpace(value)
	// 空值一律拒：agent 侧读下发缓存时对空白值按「未下发」处理（isNotBlank 过滤），
	// 存进去等于「写了个永远不会生效的值」。撤销下发请删除该行（PUT 传 null）。
	if trimmed == "" {
		return false
	}
	// 长度上限对齐 agent_setting.value 的列宽（size:4096）。
	//
	// 不设这道闸的实际后果比「存不下」更糟：直写路径会在事务提交时抛 1406 并回滚，
	// 调用方拿到的是 500 而不是可读的 400；审批路径更糟——申请能建票、能被批准，
	// 执行时才因列溢出失败，把一张人工审查过的票据变成 failed，白耗一轮审批。
	// string-list 也要管：它是整串存在同一列里，超长一样写不进去。
	if len([]rune(trimmed)) > agentSettingValueMaxRunes {
		return false
	}
	switch meta.valueType {
	case agentSettingTypeInt:
		v, err := strconv.Atoi(trimmed)
		if err != nil {
			return false
		}
		return v >= meta.min && v <= meta.max
	case agentSettingTypeBool:
		_, err := strconv.ParseBool(trimmed)
		return err == nil
	case agentSettingTypeString:
		if meta.enumOK == nil {
			return true
		}
		return meta.enumOK(trimmed)
	case agentSettingTypeStringList:
		// 列表按 agent 侧同一分隔约定（逗号或换行）切分，要求至少一个非空项。
		for _, item := range strings.FieldsFunc(trimmed, func(r rune) bool { return r == ',' || r == '\n' }) {
			if strings.TrimSpace(item) != "" {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// numericRange 构造「十进制数且落在 [lower, upper]」的校验（用于倍率 / 比例类键）。
func numericRange(lower, upper float64) func(string) bool {
	return func(raw string) bool {
		v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return false
		}
		return v >= lower && v <= upper
	}
}

// safeRelativeFileName 校验「文件名片段」：非空、不含路径分隔符与父目录引用。
//
// 这些值会参与 agent 本地落盘路径拼接，允许 `../` 或绝对路径等于让控制面写任意位置；
// 虽然 agent 侧是受管端，但下发通道不该成为路径穿越的载体。
func safeRelativeFileName(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return false
	}
	return !strings.ContainsAny(trimmed, `/\`) && !strings.Contains(trimmed, "..")
}

// safeRelativePath 校验「相对路径」：允许空（表示服务器根）、允许 `/` 分隔，但不允许绝对路径、
// 盘符与父目录引用。空串是合法语义（file-tree.target-sub-dir 空 = 服务器根）。
func safeRelativePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) || strings.Contains(trimmed, "..") {
		return false
	}
	// 盘符形式（C:）同样视为绝对路径。
	return !strings.Contains(trimmed, ":")
}

// agentSettingValueTypeName 返回面向运维的类型中文名（错误文案里用）。
func agentSettingValueTypeName(valueType string) string {
	switch valueType {
	case agentSettingTypeInt:
		return "整数"
	case agentSettingTypeBool:
		return "布尔（true / false）"
	case agentSettingTypeString:
		return "字符串"
	case agentSettingTypeStringList:
		return "字符串列表（逗号或换行分隔）"
	default:
		return valueType
	}
}

// agentSettingScopeLabel 组装作用域标识：server 级为 `环境码/serverId`，namespace 级为环境码。
//
// 它同时是**二次确认**的比对串（PUT 必须原样回填）：运维在页面上看到的作用域与真正写入的
// 作用域必须是同一个，否则「以为改了这台」的误操作会悄悄落到整个环境头上。
func agentSettingScopeLabel(namespaceCode, serverID string) string {
	code := strings.TrimSpace(namespaceCode)
	if strings.TrimSpace(serverID) == "" {
		return code
	}
	return code + "/" + strings.TrimSpace(serverID)
}
