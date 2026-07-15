package service

import (
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// renderedConfigFile 是一条 config_change 项按目标渲染出的灰度生效明文文件（数据面归一为文件项）。
type renderedConfigFile struct {
	// 落盘相对路径 = 配置文件逻辑名（ConfigFile.Name，约定为目标相对路径，如 plugins/Foo/config.yml）
	Path string
	// 灰度生效明文（灰度作用域 pin 到 to_version 渲染；明文含敏感值，绝不落日志 / 审计）
	Content string
	// 明文 sha256 小写 hex（内容寻址 blob 身份）
	SHA256 string
	// 明文字节数
	Size int64
}

// deliveryConfigRenderer 交付域配置灰度渲染器（配置灰度 restart-only，ADR-0071 决策1/2）：
// 把一条 config_change 项按目标渲染为灰度生效明文文件——落盘路径取配置文件逻辑名、灰度作用域 pin 到
// to_version 一次性选版（冻结渲染，防 head 漂移把未定稿值带进载荷）。只读调用配置中心 EffectivePlaintext
// 接缝，配置域代码零改动。
type deliveryConfigRenderer struct {
	config   *ConfigCenterService
	versions *repository.ConfigLayerVersionRepository
	files    *repository.ConfigFileRepository
}

// newDeliveryConfigRenderer 构造渲染器。
func newDeliveryConfigRenderer(config *ConfigCenterService, versions *repository.ConfigLayerVersionRepository,
	files *repository.ConfigFileRepository) *deliveryConfigRenderer {
	return &deliveryConfigRenderer{config: config, versions: versions, files: files}
}

// render 按目标 serverID 渲染一条 config_change 项的灰度生效明文文件：
// to_version → 版本行 → config_file_id → 配置文件（落盘路径 = 逻辑名）；灰度作用域 pin 到 to_version。
// EffectivePlaintext 返回未脱敏明文（目标运行需真值），调用方负责只落 blob、不落日志 / 审计。
func (r *deliveryConfigRenderer) render(item *model.ChangeOrderItem, serverID string) (*renderedConfigFile, error) {
	if item.ConfigToVersionID == nil || item.ConfigScopeKind == nil || item.ConfigScopeID == nil {
		return nil, apperr.ErrChangeConfigVersionInvalid
	}
	version, err := r.versions.FindByID(*item.ConfigToVersionID)
	if err != nil {
		return nil, err
	}
	if version == nil {
		return nil, apperr.ErrChangeConfigVersionInvalid
	}
	file, err := r.files.FindByID(version.ConfigFileID, false)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, apperr.ErrChangeConfigVersionInvalid
	}
	pins := []ConfigScopePin{{
		ScopeLevel: *item.ConfigScopeKind, ScopeRefID: *item.ConfigScopeID, VersionID: *item.ConfigToVersionID,
	}}
	content, hash, err := r.config.EffectivePlaintext(version.ConfigFileID, ConfigEffectiveTarget{ServerRef: serverID}, pins)
	if err != nil {
		return nil, err
	}
	return &renderedConfigFile{Path: file.Name, Content: content, SHA256: hash, Size: int64(len(content))}, nil
}
