package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// NamespaceTrust 是 namespace 单向互通信任关系。
type NamespaceTrust struct {
	ID              uint       `gorm:"primaryKey;autoIncrement"`
	FromNamespaceID uint       `gorm:"column:from_namespace_id;not null;uniqueIndex:uk_namespace_trust,priority:1;index:idx_namespace_trust_to,priority:2"`
	ToNamespaceID   uint       `gorm:"column:to_namespace_id;not null;uniqueIndex:uk_namespace_trust,priority:2;index:idx_namespace_trust_to,priority:1"`
	Capability      string     `gorm:"column:capability;size:32;not null;uniqueIndex:uk_namespace_trust,priority:3"`
	Status          string     `gorm:"column:status;size:16;not null;index"`
	Note            string     `gorm:"column:note;size:255;not null"`
	GrantedBy       string     `gorm:"column:granted_by;size:64;not null"`
	GrantedAt       time.Time  `gorm:"column:granted_at;not null"`
	RevokedBy       string     `gorm:"column:revoked_by;size:64"`
	RevokedAt       *time.Time `gorm:"column:revoked_at"`
	RevokeReason    string     `gorm:"column:revoke_reason;size:255"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (NamespaceTrust) TableName() string { return "namespace_trust" }

// Env 是展示维度，不参与隔离与调度。
type Env struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Code        string    `gorm:"column:code;size:64;uniqueIndex" json:"code"`
	Name        string    `gorm:"column:name;size:128;not null" json:"name"`
	DisplayName string    `gorm:"-" json:"displayName"`
	Description string    `gorm:"column:description;size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (Env) TableName() string { return "env" }

func (e *Env) BeforeSave(*gorm.DB) error {
	if e.Code == "" {
		e.Code = e.Name
	}
	if e.Name == "" {
		e.Name = e.Code
	}
	return nil
}

// EnvNamespace 是 env 到 namespace 的映射。
type EnvNamespace struct {
	ID          uint `gorm:"primaryKey;autoIncrement"`
	EnvID       uint `gorm:"column:env_id;not null;index"`
	NamespaceID uint `gorm:"column:namespace_id;not null;uniqueIndex"`
	CreatedAt   time.Time
}

func (EnvNamespace) TableName() string { return "env_namespace" }

// BCCluster 是 BC 代理集群。
type BCCluster struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	NamespaceID uint      `gorm:"column:namespace_id;not null;uniqueIndex:uk_bc_cluster_code,priority:1;index" json:"namespaceId"`
	Code        string    `gorm:"column:code;size:64;uniqueIndex:uk_bc_cluster_code,priority:2" json:"code"`
	Name        string    `gorm:"column:name;size:128;not null" json:"name"`
	DisplayName string    `gorm:"-" json:"displayName"`
	Description string    `gorm:"column:description;size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (BCCluster) TableName() string { return "bc_cluster" }

func (c *BCCluster) BeforeSave(*gorm.DB) error {
	if c.Code == "" {
		c.Code = c.Name
	}
	if c.Name == "" {
		c.Name = c.Code
	}
	return nil
}

func (c BCCluster) MarshalJSON() ([]byte, error) {
	type view struct {
		ID          uint      `json:"id"`
		NamespaceID uint      `json:"namespaceId"`
		Name        string    `json:"name"`
		Code        string    `json:"code"`
		DisplayName string    `json:"displayName"`
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"createdAt"`
		UpdatedAt   time.Time `json:"updatedAt"`
	}
	return json.Marshal(view{ID: c.ID, NamespaceID: c.NamespaceID, Name: c.Code, Code: c.Code, DisplayName: c.Name, Description: c.Description, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt})
}

// Region 是大区，隶属于一个 BC 集群。
type Region struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	BCClusterID uint      `gorm:"column:bc_cluster_id;not null;uniqueIndex:uk_region_code,priority:1;index" json:"bcClusterId"`
	Code        string    `gorm:"column:code;size:64;uniqueIndex:uk_region_code,priority:2" json:"code"`
	Name        string    `gorm:"column:name;size:128;not null" json:"name"`
	DisplayName string    `gorm:"-" json:"displayName"`
	Description string    `gorm:"column:description;size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (Region) TableName() string { return "region" }

func (r *Region) BeforeSave(*gorm.DB) error {
	if r.Code == "" {
		r.Code = r.Name
	}
	if r.Name == "" {
		r.Name = r.Code
	}
	return nil
}

func (r Region) MarshalJSON() ([]byte, error) {
	type view struct {
		ID          uint      `json:"id"`
		BCClusterID uint      `json:"bcClusterId"`
		Name        string    `json:"name"`
		Code        string    `json:"code"`
		DisplayName string    `json:"displayName"`
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"createdAt"`
		UpdatedAt   time.Time `json:"updatedAt"`
	}
	return json.Marshal(view{ID: r.ID, BCClusterID: r.BCClusterID, Name: r.Code, Code: r.Code, DisplayName: r.Name, Description: r.Description, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
}

// Zone 是小区，隶属于一个大区。
type Zone struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	RegionID    uint      `gorm:"column:region_id;not null;uniqueIndex:uk_zone_code,priority:1;index" json:"regionId"`
	Code        string    `gorm:"column:code;size:64;uniqueIndex:uk_zone_code,priority:2" json:"code"`
	Name        string    `gorm:"column:name;size:64;not null" json:"name"`
	DisplayName string    `gorm:"-" json:"displayName"`
	Description string    `gorm:"column:description;size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (Zone) TableName() string { return "zone" }

func (z *Zone) BeforeSave(*gorm.DB) error {
	if z.Code == "" {
		z.Code = z.Name
	}
	if z.Name == "" {
		z.Name = z.Code
	}
	return nil
}

func (z Zone) MarshalJSON() ([]byte, error) {
	type view struct {
		ID          uint      `json:"id"`
		RegionID    uint      `json:"regionId"`
		Name        string    `json:"name"`
		Code        string    `json:"code"`
		DisplayName string    `json:"displayName"`
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"createdAt"`
		UpdatedAt   time.Time `json:"updatedAt"`
	}
	return json.Marshal(view{ID: z.ID, RegionID: z.RegionID, Name: z.Code, Code: z.Code, DisplayName: z.Name, Description: z.Description, CreatedAt: z.CreatedAt, UpdatedAt: z.UpdatedAt})
}

// LobbyCluster 是 namespace 唯一的全局大厅集群。
// 成员由 Server.LobbyClusterID 表达；不建立外键，归属完整性由事务 service 校验。
type LobbyCluster struct {
	ID          uint `gorm:"primaryKey;autoIncrement"`
	NamespaceID uint `gorm:"column:namespace_id;not null;uniqueIndex:uk_lobby_cluster_namespace"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (LobbyCluster) TableName() string { return "lobby_cluster" }

// Server 是 v2 子服 / BC 节点资产。
type Server struct {
	ID                 uint   `gorm:"primaryKey;autoIncrement"`
	NamespaceID        uint   `gorm:"column:namespace_id;not null;uniqueIndex:uk_server_id,priority:1;index"`
	ServerID           string `gorm:"column:server_id;size:64;not null;uniqueIndex:uk_server_id,priority:2"`
	DisplayName        string `gorm:"column:display_name;size:128"`
	Kind               string `gorm:"column:kind;size:16;not null"`
	BCClusterID        *uint  `gorm:"column:bc_cluster_id;index"`
	ZoneID             *uint  `gorm:"column:zone_id;index"`
	LobbyClusterID     *uint  `gorm:"column:lobby_cluster_id;index"`
	PendingZoneID      *uint  `gorm:"column:pending_zone_id"`
	PendingBCClusterID *uint  `gorm:"column:pending_bc_cluster_id"`
	IsDefaultEntry     bool   `gorm:"column:is_default_entry;not null;default:false"`
	Draining           bool   `gorm:"column:draining;not null;default:false"`
	// 生命周期状态：active / archived / tombstoned。
	Lifecycle string `gorm:"column:lifecycle;size:16;not null;default:active;index"`
	// 归档时间；仅 archived 状态有值。
	ArchivedAt *time.Time `gorm:"column:archived_at"`
	// 发起归档的审批申请人。
	ArchivedBy string `gorm:"column:archived_by;size:128"`
	// 归档审批原因。
	ArchiveReason string `gorm:"column:archive_reason;size:255"`
	// 永久墓碑时间；墓碑记录保留以阻止同 serverId 重用。
	TombstonedAt *time.Time `gorm:"column:tombstoned_at"`
	// 发起永久墓碑的审批申请人。
	TombstonedBy string `gorm:"column:tombstoned_by;size:128"`
	// 永久墓碑审批原因。
	TombstoneReason string `gorm:"column:tombstone_reason;size:255"`
	// 永久墓碑审批请求标识。
	TombstoneApprovalRequestID string `gorm:"column:tombstone_approval_request_id;size:64;index"`
	// 永久墓碑冻结影响集合哈希。
	TombstoneImpactHash string `gorm:"column:tombstone_impact_hash;size:64"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (Server) TableName() string { return "server" }

func (s *Server) BeforeSave(*gorm.DB) error {
	if s.DisplayName == "" {
		s.DisplayName = s.ServerID
	}
	if s.Lifecycle == "" {
		s.Lifecycle = ServerLifecycleActive
	}
	return nil
}

// ServerTag 是 server 的键值标签（FR-227）：唯一键 (server_pk, tag_key)，value 允许空串。
// 以本表为 server 标签的唯一真源，FR-29 的 tag.<key>=<value> 发现过滤直接读它（不再依赖注册 metadata）。
type ServerTag struct {
	ID       uint   `gorm:"primaryKey;autoIncrement"`
	ServerPK uint   `gorm:"column:server_pk;not null;uniqueIndex:uk_server_tag,priority:1;index"`
	TagKey   string `gorm:"column:tag_key;size:32;not null;uniqueIndex:uk_server_tag,priority:2"`
	TagValue string `gorm:"column:tag_value;size:128;not null;default:''"`
	// 创建 / 最近更新时间，供标签编辑审计对照。
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (ServerTag) TableName() string { return "server_tag" }

// ServerTagLimits 是标签校验上限（FR-227 §7 拍板：key ≤32、value ≤128、单 server ≤20 个）。
const (
	ServerTagKeyMaxLen    = 32
	ServerTagValueMaxLen  = 128
	ServerTagMaxPerServer = 20
)

// AgentIdentity 是 v2 agent 身份绑定事实。
type AgentIdentity struct {
	ID           uint             `gorm:"primaryKey;autoIncrement"`
	IdentityID   string           `gorm:"column:identity_id;size:64;not null;uniqueIndex"`
	NamespaceID  uint             `gorm:"column:namespace_id;not null;index:idx_agent_identity_ns_server,priority:1;index"`
	ServerID     NullableServerID `gorm:"column:server_id;size:64;index:idx_agent_identity_ns_server,priority:2"`
	Kind         string           `gorm:"column:kind;size:16;not null"`
	Status       string           `gorm:"column:status;size:16;not null;index"`
	BootID       string           `gorm:"column:boot_id;size:36"`
	LastAddr     string           `gorm:"column:last_addr;size:320"`
	AgentVersion string           `gorm:"column:agent_version;size:32"`
	// ServerWorkDir 是 agent 上报的服务器工作目录绝对路径（FR-226）：供冲突 / 身份视图分辨「哪台、哪个目录」。
	// 旧 agent 未上报时为空串（降级展示「—」）；心跳可不携带，故仅在新值非空时覆盖。
	ServerWorkDir    string     `gorm:"column:server_work_dir;size:512"`
	PendingExpiresAt *time.Time `gorm:"column:pending_expires_at"`
	BoundAt          *time.Time `gorm:"column:bound_at"`
	StatusChangedAt  time.Time  `gorm:"column:status_changed_at;not null"`
	ConflictReason   string     `gorm:"column:conflict_reason;size:255"`
	// ConflictPeers 是并发身份冲突（Q4，FR-177）双方 boot 明细的持久化载体：
	// JSON 序列化的 [{bootId,lastAddr,lastSeenAt}] 落 TEXT 列（禁 JSON 列，守 DB 可移植）；非冲突态为空串。
	ConflictPeers    string     `gorm:"column:conflict_peers;type:text"`
	BindingSource    string     `gorm:"column:binding_source;size:24;not null;default:legacy_local"`
	LegacyMigratedAt *time.Time `gorm:"column:legacy_migrated_at"`
	// 资产绑定关闭时间；永久墓碑后保留原 serverId 供历史关联。
	AssetBindingClosedAt *time.Time `gorm:"column:asset_binding_closed_at"`
	// 关闭资产绑定的原因。
	AssetBindingCloseReason string `gorm:"column:asset_binding_close_reason;size:255"`
	// 关闭资产绑定的审批请求标识。
	AssetBindingClosedApprovalRequestID string `gorm:"column:asset_binding_closed_approval_request_id;size:64;index"`
	CreatedAt                           time.Time
	UpdatedAt                           time.Time
}

func (AgentIdentity) TableName() string { return "agent_identity" }

// AgentEndpoint 是 Agent 上报监听事实的权威记录；身份单地址仅为兼容投影。
type AgentEndpoint struct {
	ID               uint      `gorm:"primaryKey;autoIncrement"`
	AgentIdentityID  uint      `gorm:"column:agent_identity_id;not null;uniqueIndex:uk_agent_endpoint_identity_key,priority:1;index"`
	EndpointKey      string    `gorm:"column:endpoint_key;size:320;not null;uniqueIndex:uk_agent_endpoint_identity_key,priority:2"`
	Ordinal          int       `gorm:"column:ordinal;not null"`
	ReportedBindHost string    `gorm:"column:reported_bind_host;size:255;not null"`
	ReportedPort     int       `gorm:"column:reported_port;not null"`
	DetectedHost     string    `gorm:"column:detected_host;size:255;not null"`
	DetectedAddress  string    `gorm:"column:detected_address;size:320;not null"`
	OverrideAddress  *string   `gorm:"column:override_address;size:320"`
	Active           bool      `gorm:"column:active;not null;index"`
	LastSeenAt       time.Time `gorm:"column:last_seen_at;not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (AgentEndpoint) TableName() string { return "agent_endpoint" }

// NullableServerID 表示可空的身份 serverId。零值写入数据库时必须为 NULL，不能以空串伪装未分配。
type NullableServerID string

// Value 把未分配的零值持久化为 SQL NULL。
func (id NullableServerID) Value() (driver.Value, error) {
	if id == "" {
		return nil, nil
	}
	return string(id), nil
}

// Scan 从 SQL 列读取 serverId；NULL 还原为零值。
func (id *NullableServerID) Scan(value any) error {
	switch raw := value.(type) {
	case nil:
		*id = ""
		return nil
	case string:
		*id = NullableServerID(raw)
		return nil
	case []byte:
		*id = NullableServerID(string(raw))
		return nil
	default:
		return fmt.Errorf("无法读取 serverId 列类型 %T", value)
	}
}

// Assigned 返回该身份是否持有权威 serverId。
func (id NullableServerID) Assigned() bool { return id != "" }
