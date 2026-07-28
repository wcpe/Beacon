package model

import (
	"database/sql/driver"
	"fmt"
	"time"
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
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	Name        string `gorm:"column:name;size:64;not null;uniqueIndex"`
	Description string `gorm:"column:description;size:255"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Env) TableName() string { return "env" }

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
	NamespaceID uint      `gorm:"column:namespace_id;not null;uniqueIndex:uk_bc_cluster_name,priority:1;index" json:"namespaceId"`
	Name        string    `gorm:"column:name;size:64;not null;uniqueIndex:uk_bc_cluster_name,priority:2" json:"name"`
	Description string    `gorm:"column:description;size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (BCCluster) TableName() string { return "bc_cluster" }

// Region 是大区，隶属于一个 BC 集群。
type Region struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	BCClusterID uint      `gorm:"column:bc_cluster_id;not null;uniqueIndex:uk_region_name,priority:1;index" json:"bcClusterId"`
	Name        string    `gorm:"column:name;size:64;not null;uniqueIndex:uk_region_name,priority:2" json:"name"`
	Description string    `gorm:"column:description;size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (Region) TableName() string { return "region" }

// Zone 是小区，隶属于一个大区。
type Zone struct {
	ID          uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	RegionID    uint      `gorm:"column:region_id;not null;uniqueIndex:uk_zone_name,priority:1;index" json:"regionId"`
	Name        string    `gorm:"column:name;size:64;not null;uniqueIndex:uk_zone_name,priority:2" json:"name"`
	Description string    `gorm:"column:description;size:255" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (Zone) TableName() string { return "zone" }

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
	Kind               string `gorm:"column:kind;size:16;not null"`
	BCClusterID        *uint  `gorm:"column:bc_cluster_id;index"`
	ZoneID             *uint  `gorm:"column:zone_id;index"`
	LobbyClusterID     *uint  `gorm:"column:lobby_cluster_id;index"`
	PendingZoneID      *uint  `gorm:"column:pending_zone_id"`
	PendingBCClusterID *uint  `gorm:"column:pending_bc_cluster_id"`
	IsDefaultEntry     bool   `gorm:"column:is_default_entry;not null;default:false"`
	Draining           bool   `gorm:"column:draining;not null;default:false"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (Server) TableName() string { return "server" }

// AgentIdentity 是 v2 agent 身份绑定事实。
type AgentIdentity struct {
	ID               uint             `gorm:"primaryKey;autoIncrement"`
	IdentityID       string           `gorm:"column:identity_id;size:64;not null;uniqueIndex"`
	NamespaceID      uint             `gorm:"column:namespace_id;not null;index:idx_agent_identity_ns_server,priority:1;index"`
	ServerID         NullableServerID `gorm:"column:server_id;size:64;index:idx_agent_identity_ns_server,priority:2"`
	Kind             string           `gorm:"column:kind;size:16;not null"`
	Status           string           `gorm:"column:status;size:16;not null;index"`
	BootID           string           `gorm:"column:boot_id;size:36"`
	LastAddr         string           `gorm:"column:last_addr;size:320"`
	AgentVersion     string           `gorm:"column:agent_version;size:32"`
	PendingExpiresAt *time.Time       `gorm:"column:pending_expires_at"`
	BoundAt          *time.Time       `gorm:"column:bound_at"`
	StatusChangedAt  time.Time        `gorm:"column:status_changed_at;not null"`
	ConflictReason   string           `gorm:"column:conflict_reason;size:255"`
	// ConflictPeers 是并发身份冲突（Q4，FR-177）双方 boot 明细的持久化载体：
	// JSON 序列化的 [{bootId,lastAddr,lastSeenAt}] 落 TEXT 列（禁 JSON 列，守 DB 可移植）；非冲突态为空串。
	ConflictPeers    string     `gorm:"column:conflict_peers;type:text"`
	BindingSource    string     `gorm:"column:binding_source;size:24;not null;default:legacy_local"`
	LegacyMigratedAt *time.Time `gorm:"column:legacy_migrated_at"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
