package gitexport

import (
	"fmt"
	"strings"
)

// ExportMeta 是一次导出的审计元数据，用于渲染 commit message（便于 git log 追溯对应哪次发布）。
// 由发布服务在触发导出时填入，全为非机密字段（不含密钥 / 令牌 / 明文敏感内容）。
type ExportMeta struct {
	// Operator 操作者（认证身份），如 admin。
	Operator string
	// Action 动作，如 config.publish / file.rollback / zone.move（取自 model 审计动作常量）。
	Action string
	// Target 受影响对象的可读引用，如 prod/area1/mysql.yml@server:lobby-1。
	Target string
	// Version 本次发布版本号；0 表示不适用（如改派）。
	Version int64
}

// BuildCommitMessage 由审计元数据渲染 commit message（纯函数、确定性）。
// 首行为摘要（动作 + 对象），正文逐行列操作者 / 动作 / 对象 / 版本，便于 git log 检索与追溯。
// 缺失字段优雅省略（如 Version=0 不输出版本行），不引入魔法值。
func BuildCommitMessage(m ExportMeta) string {
	action := strings.TrimSpace(m.Action)
	if action == "" {
		action = "config.export" // 兜底：无动作时给个通用标识，避免空摘要
	}
	target := strings.TrimSpace(m.Target)

	var subject string
	if target != "" {
		subject = fmt.Sprintf("chore(export): %s %s", action, target)
	} else {
		subject = fmt.Sprintf("chore(export): %s", action)
	}

	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\n")
	if op := strings.TrimSpace(m.Operator); op != "" {
		fmt.Fprintf(&b, "操作者: %s\n", op)
	}
	fmt.Fprintf(&b, "动作: %s\n", action)
	if target != "" {
		fmt.Fprintf(&b, "对象: %s\n", target)
	}
	if m.Version > 0 {
		fmt.Fprintf(&b, "版本: %d\n", m.Version)
	}
	return b.String()
}
