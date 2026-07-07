package model

import "time"

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
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	NamespaceID uint   `gorm:"column:namespace_id;not null;uniqueIndex:uk_bc_cluster_name,priority:1;index"`
	Name        string `gorm:"column:name;size:64;not null;uniqueIndex:uk_bc_cluster_name,priority:2"`
	Description string `gorm:"column:description;size:255"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (BCCluster) TableName() string { return "bc_cluster" }

// Region 是大区，隶属于一个 BC 集群。
type Region struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	BCClusterID uint   `gorm:"column:bc_cluster_id;not null;uniqueIndex:uk_region_name,priority:1;index"`
	Name        string `gorm:"column:name;size:64;not null;uniqueIndex:uk_region_name,priority:2"`
	Description string `gorm:"column:description;size:255"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Region) TableName() string { return "region" }

// Zone 是小区，隶属于一个大区。
type Zone struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	RegionID    uint   `gorm:"column:region_id;not null;uniqueIndex:uk_zone_name,priority:1;index"`
	Name        string `gorm:"column:name;size:64;not null;uniqueIndex:uk_zone_name,priority:2"`
	Description string `gorm:"column:description;size:255"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Zone) TableName() string { return "zone" }

// Server 是 v2 子服 / BC 节点资产。
type Server struct {
	ID                 uint   `gorm:"primaryKey;autoIncrement"`
	NamespaceID        uint   `gorm:"column:namespace_id;not null;uniqueIndex:uk_server_id,priority:1;index"`
	ServerID           string `gorm:"column:server_id;size:64;not null;uniqueIndex:uk_server_id,priority:2"`
	Kind               string `gorm:"column:kind;size:16;not null"`
	BCClusterID        *uint  `gorm:"column:bc_cluster_id;index"`
	ZoneID             *uint  `gorm:"column:zone_id;index"`
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
	ID               uint       `gorm:"primaryKey;autoIncrement"`
	IdentityID       string     `gorm:"column:identity_id;size:64;not null;uniqueIndex"`
	NamespaceID      uint       `gorm:"column:namespace_id;not null;index:idx_agent_identity_ns_server,priority:1;index"`
	ServerID         string     `gorm:"column:server_id;size:64;not null;index:idx_agent_identity_ns_server,priority:2"`
	Kind             string     `gorm:"column:kind;size:16;not null"`
	Status           string     `gorm:"column:status;size:16;not null;index"`
	BootID           string     `gorm:"column:boot_id;size:36"`
	LastAddr         string     `gorm:"column:last_addr;size:64"`
	AgentVersion     string     `gorm:"column:agent_version;size:32"`
	PendingExpiresAt *time.Time `gorm:"column:pending_expires_at"`
	BoundAt          *time.Time `gorm:"column:bound_at"`
	StatusChangedAt  time.Time  `gorm:"column:status_changed_at;not null"`
	ConflictReason   string     `gorm:"column:conflict_reason;size:255"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (AgentIdentity) TableName() string { return "agent_identity" }
