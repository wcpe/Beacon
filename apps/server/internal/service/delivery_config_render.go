package service

import (
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// renderedConfigFile 是一份配置文件按目标聚合本单全部作用域 pin 后的灰度生效明文（数据面归一为文件项）。
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

// deliveryConfigRenderer 交付域配置灰度渲染器（ADR-0071 决策1/2）：
// 按「配置文件 × 目标」聚合同单全部 config_change 的 to_version pin，一次性冻结渲染，避免同路径工件互相覆盖，
// 并防 head 漂移把未选版本带进载荷。只读调用配置中心 EffectivePlaintext 接缝，配置域代码零改动。
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

// configRenderGroup 是同一配置文件在本单内的全部作用域 pin 聚合。
type configRenderGroup struct {
	fileID uint
	path   string
	pins   []ConfigScopePin
}

// renderAll 按目标 serverID 对每个配置文件一次性应用本单全部作用域 pin，避免同路径工件互相覆盖。
func (r *deliveryConfigRenderer) renderAll(items []model.ChangeOrderItem, serverID string) ([]renderedConfigFile, error) {
	groups, err := r.groups(items)
	if err != nil {
		return nil, err
	}
	rendered := make([]renderedConfigFile, 0, len(groups))
	for i := range groups {
		content, hash, err := r.config.EffectivePlaintext(groups[i].fileID, ConfigEffectiveTarget{ServerRef: serverID}, groups[i].pins)
		if err != nil {
			return nil, err
		}
		rendered = append(rendered, renderedConfigFile{
			Path: groups[i].path, Content: content, SHA256: hash, Size: int64(len(content)),
		})
	}
	return rendered, nil
}

// expectedPaths 从不可变目标版本解析本单配置工件应覆盖的路径集合，不触发有效配置重渲染。
func (r *deliveryConfigRenderer) expectedPaths(items []model.ChangeOrderItem) (map[string]struct{}, error) {
	groups, err := r.groups(items)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{}, len(groups))
	for i := range groups {
		paths[groups[i].path] = struct{}{}
	}
	return paths, nil
}

// groups 按配置文件聚合本单全部 config_change 的目标版本 pin，保留变更项首次出现顺序。
func (r *deliveryConfigRenderer) groups(items []model.ChangeOrderItem) ([]configRenderGroup, error) {
	groups := make([]configRenderGroup, 0)
	indexes := make(map[uint]int)
	for i := range items {
		if items[i].Kind != model.ChangeItemKindConfigChange {
			continue
		}
		version, file, err := r.resolve(&items[i])
		if err != nil {
			return nil, err
		}
		pin := ConfigScopePin{
			ScopeLevel: *items[i].ConfigScopeKind, ScopeRefID: *items[i].ConfigScopeID, VersionID: *items[i].ConfigToVersionID,
		}
		if idx, ok := indexes[version.ConfigFileID]; ok {
			groups[idx].pins = append(groups[idx].pins, pin)
			continue
		}
		indexes[version.ConfigFileID] = len(groups)
		groups = append(groups, configRenderGroup{fileID: version.ConfigFileID, path: file.Name, pins: []ConfigScopePin{pin}})
	}
	return groups, nil
}

// resolve 校验配置变更项并解析其目标版本与配置文件。
func (r *deliveryConfigRenderer) resolve(item *model.ChangeOrderItem) (*model.ConfigLayerVersion, *model.ConfigFile, error) {
	if item.ConfigToVersionID == nil || item.ConfigScopeKind == nil || item.ConfigScopeID == nil {
		return nil, nil, apperr.ErrChangeConfigVersionInvalid
	}
	version, err := r.versions.FindByID(*item.ConfigToVersionID)
	if err != nil {
		return nil, nil, err
	}
	if version == nil {
		return nil, nil, apperr.ErrChangeConfigVersionInvalid
	}
	file, err := r.files.FindByID(version.ConfigFileID, false)
	if err != nil {
		return nil, nil, err
	}
	if file == nil {
		return nil, nil, apperr.ErrChangeConfigVersionInvalid
	}
	return version, file, nil
}
