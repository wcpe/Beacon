package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// —— 自由文本列的原因串收口漏斗（交付域与 file_sync 共用）——
//
// 这里的函数是「agent 上报原文 / 控制面自拼原因串」落到定长或 TEXT 列前的**唯一**收口点。
// 为什么必须集中：超列宽在 MySQL 严格模式下让整条 UPDATE 报 1406 → CAS 返回 err → 目标原地
// 卡在原状态，下一 tick 用同一个超宽值重试，形成稳定失败循环——比直接报错更难查。
// 各调用点自行截断会让口径分叉（有人按 rune、有人按字节、有人忘了截），故一律走这里。
//
// 归属说明：这些件原先定义在 file_sync_execution.go（file_sync 域先用到），但交付域的
// casTarget / casRollbackFrom 也走同一套漏斗，留在 file_sync 里会让交付侧「依赖文件同步的私有件」。
// 现独立成文件，交付域与 file_sync 域各自引用，file_sync 侧的 fileSync*MaxRunes 仍留在原处。

// changeTargetErrorMaxRunes 是 change_target.error / rollback_error 的字符上限（列宽 1024）。
//
// 留出余量取 1000：MySQL 的 size 在 utf8mb4 下按字符计，前缀固定文案与截断标记也要占位。
// 超列宽在严格模式下会让整条 UPDATE 报 1406 → CAS 返回 err → 目标原地卡在 activating，
// 下一 tick 用同一个超宽值重试，形成稳定失败循环。控制面自己拼的原因串同样要过这道收口。
const changeTargetErrorMaxRunes = 1000

// changeTargetReloadReportMaxBytes 是 change_target.reload_report（type:text，65535 字节）的字节上限。
//
// 为什么必须收口：单插件最坏 ≈（reason 512 + detailJson 4096）字符 × 3 字节（utf8mb4 中文）≈ 13.8KB，
// 约 5 个插件同时自报满长明细就越过 64KB。它与 error 落在**同一条** CAS UPDATE 里，
// 溢出就不是「明细缺一点」，而是整条迁移失败（见 changeTargetErrorMaxRunes 的失败循环说明）。
//
// 取 56000 留余量：告警侧 detail 同为 TEXT，还要并排放 error（1000 字符 ≈ 4KB）与归属字段，
// 单靠「没超 65535」不能保证告警能写进去。
const changeTargetReloadReportMaxBytes = 56000

// reloadReportMaxPlugins 是超限退化形态里保留的插件明细条数上限（与字节上限构成双重约束）。
const reloadReportMaxPlugins = 10

// 超限退化形态里单插件字段的字符上限：与 agent 侧上报上限对齐（reason 512），
// 让「前几条失败原因」在字节预算内尽量留全，而不是被第一条满长明细独吞预算。
const (
	reloadReportPluginReasonMaxRunes = 512
	reloadReportPluginDetailMaxRunes = 512
)

// capMapError 就地收口 updates 里的原因串（键为 key），把值截到 change_target 列宽内。
//
// 只在值是 string 时生效；其它类型（表达式、时间戳）原样保留——但**必须留痕**：
// 这是护栏代码，静默 no-op 是最难查的失效形态（哪天有人把值换成 sql.NullString / *string /
// gorm.Expr，收口会安静停止生效，直到生产上撞出 1406 才被发现）。
func capMapError(updates map[string]any, key string) {
	raw, has := updates[key]
	if !has {
		return
	}
	text, okc := raw.(string)
	if !okc {
		slog.Warn("原因串收口跳过：值不是字符串，护栏未生效", "键", key, "类型", fmt.Sprintf("%T", raw))
		return
	}
	updates[key] = capReasonRunes(text, changeTargetErrorMaxRunes)
}

// capMapReloadReport 就地收口 updates 里的逐插件重载报告（TEXT 列），超限退化为截断形态。
func capMapReloadReport(updates map[string]any, key string) {
	raw, has := updates[key]
	if !has {
		return
	}
	text, okc := raw.(string)
	if !okc {
		slog.Warn("重载明细收口跳过：值不是字符串，护栏未生效", "键", key, "类型", fmt.Sprintf("%T", raw))
		return
	}
	updates[key] = capReloadReportJSON(text, changeTargetReloadReportMaxBytes)
}

// capDeliveryTargetUpdates 是交付目标行自由文本的**唯一**收口入口（casTarget / casRollbackFrom 共用）：
// errorKey 指定该路径的原因列（正推落 error、回滚落 rollback_error），reload_report 两条路径同名列。
//
// 就地写回而不是只收「落库副本」：随后读 updates 发告警的路径因此拿到与库里完全一致的值。
// 此前只收拷贝副本，告警拿到的是未收口原文——同一个原因在 DB 与告警里两个口径。
func capDeliveryTargetUpdates(updates map[string]any, errorKey string) {
	capMapError(updates, errorKey)
	capMapReloadReport(updates, "reload_report")
}

// reloadReportTruncated 是 reload_report 超限后的退化形态：保留最关键的事实
// （总数 / 成功数 / 失败数 / 前若干条插件明细），并显式标注 truncated，
// 让前端与运维一眼看出「明细不全」，而不是把残缺当全量。
type reloadReportTruncated struct {
	Total       int                    `json:"total"`
	OKCount     int                    `json:"okCount"`
	FailedCount int                    `json:"failedCount"`
	TimeoutMs   int64                  `json:"timeoutMs,omitempty"`
	Truncated   bool                   `json:"truncated"`
	Plugins     []deliveryReloadPlugin `json:"plugins"`
}

// capReloadReportJSON 把超宽的重载报告收口到 maxBytes 内：未超限原样返回（保住 agent 上报的全部字段），
// 超限则退化为 reloadReportTruncated（失败项优先 + 条数上限 + 字节上限）。
//
// **截断只影响展示，不影响判定**：一票否决（reloadPluginsFailedReason / reloadVersionMismatch）
// 一律基于收口**之前**的原始报告完成——调用方先判定、后落库，故截断绝不会把「有插件失败」判成成功。
func capReloadReportJSON(raw string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(raw) <= maxBytes {
		return raw // 快路径：未超限不动原文，避免无谓重编码与未知字段丢失
	}
	var report deliveryReloadReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		// 连解析都失败的超宽值：没有可保留的结构事实，按字节安全的方式压成一段可读文本。
		// 单 rune 最多 4 字节，除以 4 保证结果字节数不超过上限。
		return capReasonRunes(raw, maxBytes/4)
	}
	out := reloadReportTruncated{
		Total: report.Total, OKCount: report.OKCount, FailedCount: report.FailedCount,
		TimeoutMs: report.TimeoutMs, Truncated: true,
		Plugins: make([]deliveryReloadPlugin, 0, reloadReportMaxPlugins),
	}
	skeleton, err := json.Marshal(out)
	if err != nil {
		slog.Warn("重载明细截断骨架序列化失败，退化为文本截断", "错误", err)
		return capReasonRunes(raw, maxBytes/4)
	}
	// 预算按「骨架 + 逐条累加」记账，而不是每条都整份重编码：
	// 每追加一条新增 len(entry)+1 字节（+1 为分隔逗号，保守计入），故最终长度必不超 maxBytes。
	budget := maxBytes - len(skeleton)
	for _, p := range pluginsFailedFirst(report.Plugins) {
		if len(out.Plugins) >= reloadReportMaxPlugins {
			break
		}
		entry := capReloadPlugin(p)
		encoded, e := json.Marshal(entry)
		if e != nil {
			continue // 单条编码失败不拖垮整份报告：其余插件仍可展示
		}
		if len(encoded)+1 > budget {
			break // 预算用尽：宁可少展示一条，也不让整条 UPDATE 超宽
		}
		budget -= len(encoded) + 1
		out.Plugins = append(out.Plugins, entry)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return capReasonRunes(raw, maxBytes/4)
	}
	if len(encoded) > maxBytes { // 理论不可达（预算已上界），保守兜底：绝不放出超宽值
		return capReasonRunes(raw, maxBytes/4)
	}
	return string(encoded)
}

// capReloadPlugin 收口单插件明细里的自由文本字段（截断形态专用），避免一条满长明细吞掉全部预算。
func capReloadPlugin(p deliveryReloadPlugin) deliveryReloadPlugin {
	p.Reason = capReasonRunes(p.Reason, reloadReportPluginReasonMaxRunes)
	p.DetailJSON = capReasonRunes(p.DetailJSON, reloadReportPluginDetailMaxRunes)
	return p
}

// pluginsFailedFirst 把非 ok 插件排到前面（各自保持原相对顺序）：
// 截断时最该保留的是失败项明细，成功项只是佐证。只做重排，不改任何插件内容。
func pluginsFailedFirst(plugins []deliveryReloadPlugin) []deliveryReloadPlugin {
	out := make([]deliveryReloadPlugin, 0, len(plugins))
	for i := range plugins {
		if plugins[i].Status != deliveryReloadStatusOK {
			out = append(out, plugins[i])
		}
	}
	for i := range plugins {
		if plugins[i].Status == deliveryReloadStatusOK {
			out = append(out, plugins[i])
		}
	}
	return out
}

// capReasonRunes 按 **rune** 截断原因串，超限时显式标注截断（不静默丢信息）。
//
// 必须按 rune 而非字节切：`text[:480]` 遇到中文会把多字节字符切成两半，产出非法 UTF-8
// 落库（change_target.error），后续 JSON 序列化与前端展示都会出错。
//
// 契约：返回值的 rune 数**恒 <= max**。max 小到放不下截断标记时硬截断，不返回超长标记。
func capReasonRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	const marker = "…（已截断）"
	keep := limit - len([]rune(marker))
	if keep <= 0 {
		return string(r[:limit])
	}
	return string(r[:keep]) + marker
}
