package service

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// ObservationScope 是单次观测请求冻结的 namespace 集合。
// All 只在调用方完全省略范围时为真；空 NamespaceIDs 是合法的空映射。
type ObservationScope struct {
	All            bool
	NamespaceIDs   []uint
	NamespaceCodes []string
	Fingerprint    string
}

// Contains 判断 namespace 是否位于冻结范围内。
func (s ObservationScope) Contains(namespaceID uint) bool {
	if s.All {
		return true
	}
	for _, id := range s.NamespaceIDs {
		if id == namespaceID {
			return true
		}
	}
	return false
}

// Empty 判断冻结范围是否为空映射。
func (s ObservationScope) Empty() bool { return !s.All && len(s.NamespaceIDs) == 0 }

// ObservationScopeResolver 统一解析 envId/namespaceId，避免各观测端点自行回退或重复查映射。
type ObservationScopeResolver struct {
	envs       *repository.EnvRepository
	namespaces *repository.NamespaceRepository
}

// NewObservationScopeResolver 构造服务端唯一的观测范围解析器。
func NewObservationScopeResolver(envs *repository.EnvRepository, namespaces *repository.NamespaceRepository) *ObservationScopeResolver {
	return &ObservationScopeResolver{envs: envs, namespaces: namespaces}
}

// Resolve 把原始 query 解析为本请求不可变的范围快照。
func (r *ObservationScopeResolver) Resolve(rawEnvID, rawNamespaceID string) (ObservationScope, error) {
	envID, hasEnv, err := observationScopeID(rawEnvID)
	if err != nil {
		return ObservationScope{}, err
	}
	namespaceID, hasNamespace, err := observationScopeID(rawNamespaceID)
	if err != nil {
		return ObservationScope{}, err
	}
	if !hasEnv && !hasNamespace {
		return observationScope(true, nil), nil
	}
	if hasEnv {
		env, err := r.envs.FindByID(envID)
		if err != nil {
			return ObservationScope{}, err
		}
		if env == nil {
			return ObservationScope{}, apperr.ErrObservationScopeStale
		}
	}
	if hasNamespace {
		ns, err := r.namespaces.FindByID(namespaceID)
		if err != nil {
			return ObservationScope{}, err
		}
		if ns == nil {
			return ObservationScope{}, apperr.ErrObservationScopeStale
		}
	}
	if !hasEnv {
		return r.scopeFromIDs([]uint{namespaceID})
	}
	mappings, err := r.envs.ListMappingsByEnv(envID)
	if err != nil {
		return ObservationScope{}, err
	}
	ids := make([]uint, 0, len(mappings))
	for _, mapping := range mappings {
		if hasNamespace && mapping.NamespaceID == namespaceID {
			return r.scopeFromIDs([]uint{namespaceID})
		}
		ids = append(ids, mapping.NamespaceID)
	}
	if hasNamespace {
		return ObservationScope{}, apperr.ErrObservationScopeStale
	}
	return r.scopeFromIDs(ids)
}

func observationScopeID(raw string) (uint, bool, error) {
	if raw == "" {
		return 0, false, nil
	}
	value, err := strconv.ParseUint(raw, 10, 0)
	if err != nil || value == 0 {
		return 0, false, apperr.ErrInvalidObservationScope
	}
	return uint(value), true, nil
}

func observationScope(all bool, ids []uint) ObservationScope {
	copyIDs := append([]uint(nil), ids...)
	sort.Slice(copyIDs, func(i, j int) bool { return copyIDs[i] < copyIDs[j] })
	parts := make([]string, len(copyIDs))
	for i, id := range copyIDs {
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	base := "all"
	if !all {
		base = "namespaces:" + strings.Join(parts, ",")
	}
	digest := sha256.Sum256([]byte(base))
	return ObservationScope{All: all, NamespaceIDs: copyIDs, Fingerprint: fmt.Sprintf("%x", digest)}
}

func (r *ObservationScopeResolver) scopeFromIDs(ids []uint) (ObservationScope, error) {
	scope := observationScope(false, ids)
	items, err := r.namespaces.FindByIDs(scope.NamespaceIDs)
	if err != nil {
		return ObservationScope{}, err
	}
	if len(items) != len(scope.NamespaceIDs) {
		return ObservationScope{}, apperr.ErrObservationScopeStale
	}
	scope.NamespaceCodes = make([]string, 0, len(items))
	for _, item := range items {
		scope.NamespaceCodes = append(scope.NamespaceCodes, item.Code)
	}
	return scope, nil
}
