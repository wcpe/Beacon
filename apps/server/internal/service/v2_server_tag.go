package service

import (
	"regexp"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 本文件实现 server 键值标签（FR-227）：server_tag 为唯一真源，供资产展示、列表交集筛选与
// FR-29 发现过滤共用；增删按 key 直改 + 强审计（低风险直执，见 spec §7）。

// ServerTagView 是 server 标签的对外视图（key/value），按 key 稳定排序。
type ServerTagView struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// serverTagKeyPattern 限定标签 key 字符集（FR-227 §3：字母 / 数字 / _ . -）。
var serverTagKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// normalizeServerTags 校验并规整标签映射：key 非空、符合字符集与长度上限，value 长度受限。
func normalizeServerTags(tags map[string]string) (map[string]string, error) {
	if len(tags) == 0 {
		return nil, apperr.ErrInvalidParam
	}
	out := make(map[string]string, len(tags))
	for k, v := range tags {
		key := strings.TrimSpace(k)
		if key == "" || len(key) > model.ServerTagKeyMaxLen || !serverTagKeyPattern.MatchString(key) {
			return nil, apperr.ErrInvalidParam
		}
		if len(v) > model.ServerTagValueMaxLen {
			return nil, apperr.ErrInvalidParam
		}
		out[key] = v
	}
	return out, nil
}

// loadServerTagsForServer 读单台 server 的全部标签（按 key 升序）。
func loadServerTagsForServer(db *gorm.DB, serverPK uint) ([]ServerTagView, error) {
	var rows []model.ServerTag
	if err := db.Where("server_pk = ?", serverPK).Order("tag_key ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return serverTagViews(rows), nil
}

// loadServerTags 批量取多台 server 的标签（serverPK → 排序后标签），禁循环内查库（N+1）。
func loadServerTags(db *gorm.DB, serverPKs []uint) (map[uint][]ServerTagView, error) {
	byServer := map[uint][]ServerTagView{}
	if len(serverPKs) == 0 {
		return byServer, nil
	}
	var rows []model.ServerTag
	if err := db.Where("server_pk IN ?", serverPKs).Order("server_pk ASC, tag_key ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		byServer[r.ServerPK] = append(byServer[r.ServerPK], ServerTagView{Key: r.TagKey, Value: r.TagValue})
	}
	return byServer, nil
}

func serverTagViews(rows []model.ServerTag) []ServerTagView {
	out := make([]ServerTagView, 0, len(rows))
	for i := range rows {
		out = append(out, ServerTagView{Key: rows[i].TagKey, Value: rows[i].TagValue})
	}
	return out
}

// SetServerTagsParams 是按 key 增改标签的入参（未出现的既有 key 保留）。
type SetServerTagsParams struct {
	ServerID string
	Tags     map[string]string
	Operator string
	ClientIP string
}

// SetServerTags 按 key 增改 server 标签（FR-227）：重复 key 覆盖而非新增行；合并后超单机上限则整体拒绝。
func (s *V2ControlPlaneService) SetServerTags(p SetServerTagsParams) (*ServerView, error) {
	if strings.TrimSpace(p.ServerID) == "" {
		return nil, apperr.ErrInvalidParam
	}
	tags, err := normalizeServerTags(p.Tags)
	if err != nil {
		return nil, err
	}
	var view *ServerView
	err = s.db.Transaction(func(tx *gorm.DB) error {
		server, err := findServerByServerID(tx, p.ServerID)
		if err != nil {
			return err
		}
		existing, err := loadServerTagsForServer(tx, server.ID)
		if err != nil {
			return err
		}
		oldByKey := make(map[string]string, len(existing))
		for _, t := range existing {
			oldByKey[t.Key] = t.Value
		}
		// 合并后校验单机标签数上限（新 key 才增计数）。
		merged := make(map[string]struct{}, len(oldByKey)+len(tags))
		for k := range oldByKey {
			merged[k] = struct{}{}
		}
		for k := range tags {
			merged[k] = struct{}{}
		}
		if len(merged) > model.ServerTagMaxPerServer {
			return apperr.ErrInvalidParam
		}
		keys := make([]string, 0, len(tags))
		for k := range tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := tags[k]
			tag := model.ServerTag{ServerPK: server.ID, TagKey: k, TagValue: v}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "server_pk"}, {Name: "tag_key"}},
				DoUpdates: clause.AssignmentColumns([]string{"tag_value", "updated_at"}),
			}).Create(&tag).Error; err != nil {
				return err
			}
			detail := auditJSON(map[string]string{
				"serverId": server.ServerID, "key": k, "oldValue": oldByKey[k], "newValue": v,
			})
			if err := createAudit(tx, model.AuditLog{
				Operator: operatorOrSystem(p.Operator), Action: model.ActionServerTagUpdated,
				TargetType: model.TargetTypeServer, TargetRef: server.ServerID,
				Detail: detail, Result: model.ResultOK, ClientIP: p.ClientIP,
			}); err != nil {
				return err
			}
		}
		view, err = enrichSingleServer(tx, *server)
		return err
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// DeleteServerTagParams 是删除单个标签的入参。
type DeleteServerTagParams struct {
	ServerID string
	TagKey   string
	Operator string
	ClientIP string
}

// DeleteServerTag 删除 server 的单个标签（FR-227）；不存在仍幂等成功（不报 404），写审计。
func (s *V2ControlPlaneService) DeleteServerTag(p DeleteServerTagParams) (*ServerView, error) {
	key := strings.TrimSpace(p.TagKey)
	if strings.TrimSpace(p.ServerID) == "" || key == "" || len(key) > model.ServerTagKeyMaxLen {
		return nil, apperr.ErrInvalidParam
	}
	var view *ServerView
	err := s.db.Transaction(func(tx *gorm.DB) error {
		server, err := findServerByServerID(tx, p.ServerID)
		if err != nil {
			return err
		}
		var existing model.ServerTag
		oldValue := ""
		found := true
		if err := tx.Where("server_pk = ? AND tag_key = ?", server.ID, key).First(&existing).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				found = false
			} else {
				return err
			}
		} else {
			oldValue = existing.TagValue
			if err := tx.Delete(&existing).Error; err != nil {
				return err
			}
		}
		if found {
			detail := auditJSON(map[string]string{
				"serverId": server.ServerID, "key": key, "oldValue": oldValue, "newValue": "",
			})
			if err := createAudit(tx, model.AuditLog{
				Operator: operatorOrSystem(p.Operator), Action: model.ActionServerTagUpdated,
				TargetType: model.TargetTypeServer, TargetRef: server.ServerID,
				Detail: detail, Result: model.ResultOK, ClientIP: p.ClientIP,
			}); err != nil {
				return err
			}
		}
		view, err = enrichSingleServer(tx, *server)
		return err
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// matchTagServerPKs 返回同时命中全部标签的 server 主键集合（多 tag 取交集）；空 tags 返回空集。
func matchTagServerPKs(db *gorm.DB, tags map[string]string) (map[uint]struct{}, error) {
	var intersect map[uint]struct{}
	for k, v := range tags {
		var pks []uint
		if err := db.Model(&model.ServerTag{}).Where("tag_key = ? AND tag_value = ?", k, v).Pluck("server_pk", &pks).Error; err != nil {
			return nil, err
		}
		set := make(map[uint]struct{}, len(pks))
		for _, pk := range pks {
			set[pk] = struct{}{}
		}
		if intersect == nil {
			intersect = set
			continue
		}
		for pk := range intersect {
			if _, ok := set[pk]; !ok {
				delete(intersect, pk)
			}
		}
	}
	if intersect == nil {
		intersect = map[uint]struct{}{}
	}
	return intersect, nil
}

// ServerRefsMatchingTags 返回同时命中全部标签的 (namespaceId, serverId) 去重集合（多 tag 取交集，FR-227）。
// namespaceID=0 表示不限 namespace；空 tags 返回 nil（表示不过滤）。
func ServerRefsMatchingTags(db *gorm.DB, namespaceID uint, tags map[string]string) (map[onlineKey]struct{}, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	intersect, err := matchTagServerPKs(db, tags)
	if err != nil {
		return nil, err
	}
	out := map[onlineKey]struct{}{}
	if len(intersect) == 0 {
		return out, nil
	}
	pks := make([]uint, 0, len(intersect))
	for pk := range intersect {
		pks = append(pks, pk)
	}
	var servers []model.Server
	q := db.Select("id", "namespace_id", "server_id").Where("id IN ?", pks)
	if namespaceID != 0 {
		q = q.Where("namespace_id = ?", namespaceID)
	}
	if err := q.Find(&servers).Error; err != nil {
		return nil, err
	}
	for i := range servers {
		out[onlineKey{namespaceID: servers[i].NamespaceID, serverID: servers[i].ServerID}] = struct{}{}
	}
	return out, nil
}

// ServerCodesMatchingTags 返回同时命中全部标签的「namespaceCode/serverId」去重集合（FR-29 发现路径用，
// 实例注册表以 namespace code 定位）。namespaceCode 为空表示不限；空 tags 返回 nil。
func ServerCodesMatchingTags(db *gorm.DB, namespaceCode string, tags map[string]string) (map[string]struct{}, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	intersect, err := matchTagServerPKs(db, tags)
	if err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	if len(intersect) == 0 {
		return out, nil
	}
	pks := make([]uint, 0, len(intersect))
	for pk := range intersect {
		pks = append(pks, pk)
	}
	var servers []model.Server
	if err := db.Select("namespace_id", "server_id").Where("id IN ?", pks).Find(&servers).Error; err != nil {
		return nil, err
	}
	codeByID, err := namespaceCodesByID(db, servers)
	if err != nil {
		return nil, err
	}
	for i := range servers {
		code := codeByID[servers[i].NamespaceID]
		if namespaceCode != "" && code != namespaceCode {
			continue
		}
		out[code+"/"+servers[i].ServerID] = struct{}{}
	}
	return out, nil
}

// namespaceCodesByID 批量取 server 涉及的 namespace id → code 映射。
func namespaceCodesByID(db *gorm.DB, servers []model.Server) (map[uint]string, error) {
	byID := map[uint]string{}
	if len(servers) == 0 {
		return byID, nil
	}
	ids := make([]uint, 0, len(servers))
	seen := map[uint]struct{}{}
	for i := range servers {
		if _, ok := seen[servers[i].NamespaceID]; ok {
			continue
		}
		seen[servers[i].NamespaceID] = struct{}{}
		ids = append(ids, servers[i].NamespaceID)
	}
	var nss []model.Namespace
	if err := db.Select("id", "code").Where("id IN ?", ids).Find(&nss).Error; err != nil {
		return nil, err
	}
	for i := range nss {
		byID[nss[i].ID] = nss[i].Code
	}
	return byID, nil
}
