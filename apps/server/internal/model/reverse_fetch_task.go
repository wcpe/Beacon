package model

import (
	"time"

	"gorm.io/gorm"
)

// ReverseFetchTask 是反向抓取受管任务（FR-58，见 ADR-0037）：把"一次性整批抓取"升级为
// "受管任务 + 两段式（先扫清单、再抓选定）"。任务是真源、agent_command 是其执行手段（一对多）。
// 状态机 scanning → pending-review → fetching → ingesting → done；旁出 failed/cancelled/expired。
//
// GORM 可移植：状态落 VARCHAR + 应用层校验、清单 / 选定集落 TEXT（不用 ENUM/JSON 列）、
// 单实例互斥沿用软删哨兵唯一键范式（ActiveAt 哨兵 = 非终态、置真实时间 = 已终结，允许同服多个历史任务并存）。
type ReverseFetchTask struct {
	// 自增主键，兼作任务 id
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 目标实例所属环境
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;index:idx_rft_ns_server,priority:1;uniqueIndex:uk_rft_active,priority:1"`
	// 目标实例 serverId
	ServerID string `gorm:"column:server_id;size:128;not null;index:idx_rft_ns_server,priority:2;uniqueIndex:uk_rft_active,priority:2"`
	// 落库覆盖层：group / server（沿 FR-39 反向抓取语义）
	Scope string `gorm:"column:scope;size:16;not null"`
	// 所属大区
	GroupCode string `gorm:"column:group_code;size:64;not null"`
	// scope=server 时的目标 serverId（group 层留空）
	ScopeTarget string `gorm:"column:scope_target;size:128"`
	// 状态机当前态（落 VARCHAR + 应用层校验）
	Status string `gorm:"column:status;size:16;not null"`
	// scan 命令 id（引用 agent_command.id；0=未下发）
	ScanCommandID uint `gorm:"column:scan_command_id"`
	// submit 命令 id（引用 agent_command.id；0=未下发）
	SubmitCommandID uint `gorm:"column:submit_command_id"`
	// 扫描清单 JSON（TEXT）：{totalFiles,totalBytes,skipped,files:[{path,size,isText,overThreshold}]}；
	// 过期 / 终结后清空瞬态，避免大树清单 TEXT 长期滞留
	Manifest string `gorm:"column:manifest;type:text"`
	// 提交时选定 path 的 JSON 数组（TEXT）
	SelectedPaths string `gorm:"column:selected_paths;type:text"`
	// 冲突审核暂存内容 JSON（TEXT，瞬态，FR-59）：conflict-review 期暂存 submit 回传的全部选定内容
	// （{path→content}），供逐文件 diff 与 resolve 落库；resolve 完成 / 取消 / 过期后清空（同 imprint_content 范式）。
	// 与扫描清单的 manifest（元信息、无内容）分立——这是冲突审核期的内容暂存，生命周期到 resolve 即止。
	SubmitContent string `gorm:"column:submit_content;type:text"`
	// 清单总文件数（进度用）
	TotalFiles int `gorm:"column:total_files"`
	// 选定文件数
	SelectedCount int `gorm:"column:selected_count"`
	// 超单文件阈值的文件数（清单红标计数）
	OverThresholdCount int `gorm:"column:over_threshold_count"`
	// agent 侧已跳过的文件数（.jar / 二进制 / 不安全路径）
	SkippedCount int `gorm:"column:skipped_count"`
	// 触发操作者（admin 认证身份，非手填）
	Operator string `gorm:"column:operator;size:128;not null"`
	// 备注 / 结果摘要（无敏感文件内容）：如取消原因、done 计数；与 LastError（失败明细）分立
	Note string `gorm:"column:note;size:512"`
	// 失败原因明细（FR-87，无敏感文件内容）：agent 回传 scan/submit 读盘错误或控制面入库失败时写入；
	// 与 Note（结果 / 取消摘要）分立——专记失败错因，供任务台 failed 任务展示。
	LastError string `gorm:"column:last_error;size:512"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）；每次状态迁移刷新
	UpdatedAt time.Time
	// 互斥哨兵：未终结为哨兵值（同 (ns, serverId) 至多一条非终态），终结时置真实时间允许并存历史任务。
	// 复用 SoftDeleteSentinel 软删哨兵唯一键范式（见 ADR-0008 / server_drain），保证 GORM 可移植。
	ActiveAt time.Time `gorm:"column:active_at;not null;uniqueIndex:uk_rft_active,priority:3"`
}

// TableName 固定表名为 reverse_fetch_task。
func (ReverseFetchTask) TableName() string { return "reverse_fetch_task" }

// BeforeCreate 在插入前为非终态任务写入互斥哨兵值（与 server_drain 软删哨兵同源范式）。
func (t *ReverseFetchTask) BeforeCreate(*gorm.DB) error {
	if t.ActiveAt.IsZero() {
		t.ActiveAt = SoftDeleteSentinel
	}
	return nil
}
