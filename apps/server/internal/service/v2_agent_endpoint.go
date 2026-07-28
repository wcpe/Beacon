package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

const maxProxyListeners = 32

// AgentEndpointReport 是 Agent 上报的本地监听事实；外部地址由控制面派生。
type AgentEndpointReport struct {
	BindHost string
	Port     int
	Ordinal  int
}

// AgentEndpointView 是 endpoint 的管理面与注册响应契约。
type AgentEndpointView struct {
	EndpointKey      string    `json:"endpointKey"`
	Ordinal          int       `json:"ordinal"`
	ReportedBindHost string    `json:"reportedBindHost"`
	ReportedPort     int       `json:"reportedPort"`
	DetectedAddress  string    `json:"detectedAddress"`
	OverrideAddress  *string   `json:"overrideAddress"`
	EffectiveAddress string    `json:"effectiveAddress"`
	Source           string    `json:"source"`
	Active           bool      `json:"active"`
	LastSeenAt       time.Time `json:"lastSeenAt"`
}

// AgentEndpointOverrideParams 是管理员逐 endpoint 覆盖请求。
type AgentEndpointOverrideParams struct {
	OverrideAddress *string
	Reason          string
	Operator        string
	ClientIP        string
	TraceID         string
}

// syncAgentEndpoints 在注册事务内以完整集合对账 endpoint，并回写兼容单地址投影。
func syncAgentEndpoints(tx *gorm.DB, ident *model.AgentIdentity, p AgentRegisterV2Params, now time.Time) ([]AgentEndpointView, error) {
	reports, managed, err := endpointReportsForRegistration(p)
	if err != nil {
		return nil, err
	}
	if !managed {
		return nil, nil
	}
	// 仅 HTTP handler 拥有原始 TCP 对端事实。历史内部直调（主要是旧服务测试）不补造 endpoint，
	// 绝不能退回把 Addr 请求体 host 当探测来源；生产注册路径必由 handler 传入 DetectedHost。
	if strings.TrimSpace(p.DetectedHost) == "" {
		return nil, nil
	}
	detectedHost, err := normalizeBindHost(p.DetectedHost)
	if err != nil || detectedHost == "" {
		return nil, apperr.ErrInvalidParam
	}
	keys := make([]string, 0, len(reports))
	for _, report := range reports {
		endpoint := model.AgentEndpoint{
			AgentIdentityID: ident.ID, EndpointKey: report.key, Ordinal: report.ordinal,
			ReportedBindHost: report.bindHost, ReportedPort: report.port,
			DetectedHost: detectedHost, DetectedAddress: net.JoinHostPort(detectedHost, strconv.Itoa(report.port)),
			Active: true, LastSeenAt: now,
		}
		var existing model.AgentEndpoint
		err := tx.Where("agent_identity_id = ? AND endpoint_key = ?", ident.ID, report.key).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&endpoint).Error; err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		} else {
			existing.Ordinal = endpoint.Ordinal
			existing.ReportedBindHost = endpoint.ReportedBindHost
			existing.ReportedPort = endpoint.ReportedPort
			existing.DetectedHost = endpoint.DetectedHost
			existing.DetectedAddress = endpoint.DetectedAddress
			existing.Active = true
			existing.LastSeenAt = endpoint.LastSeenAt
			if err := tx.Save(&existing).Error; err != nil {
				return nil, err
			}
		}
		keys = append(keys, report.key)
	}
	if err := tx.Model(&model.AgentEndpoint{}).Where("agent_identity_id = ? AND endpoint_key NOT IN ?", ident.ID, keys).
		Update("active", false).Error; err != nil {
		return nil, err
	}
	return refreshCompatibilityAddress(tx, ident)
}

type normalizedEndpointReport struct {
	key      string
	bindHost string
	port     int
	ordinal  int
}

func endpointReportsForRegistration(p AgentRegisterV2Params) ([]normalizedEndpointReport, bool, error) {
	if p.Kind == model.ServerKindBackend && p.ListenersProvided {
		return nil, false, apperr.ErrInvalidParam
	}
	if p.Kind == model.ServerKindProxy && p.ListenPort != nil {
		return nil, false, apperr.ErrInvalidParam
	}
	if p.Kind == model.ServerKindBackend && p.ListenPort != nil {
		return normalizeEndpointReports([]AgentEndpointReport{{BindHost: "", Port: *p.ListenPort, Ordinal: 0}}, true)
	}
	if p.Kind == model.ServerKindProxy && p.ListenersProvided {
		return normalizeEndpointReports(p.Listeners, false)
	}
	if strings.TrimSpace(p.Addr) == "" {
		return nil, false, nil
	}
	_, port, err := parseAddress(p.Addr)
	if err != nil {
		return nil, false, apperr.ErrInvalidParam
	}
	return normalizeEndpointReports([]AgentEndpointReport{{BindHost: "", Port: port, Ordinal: 0}}, p.Kind == model.ServerKindBackend)
}

func normalizeEndpointReports(reports []AgentEndpointReport, backend bool) ([]normalizedEndpointReport, bool, error) {
	if backend && len(reports) != 1 || !backend && (len(reports) == 0 || len(reports) > maxProxyListeners) {
		return nil, false, apperr.ErrInvalidParam
	}
	seen := map[string]struct{}{}
	result := make([]normalizedEndpointReport, 0, len(reports))
	for _, report := range reports {
		if report.Port < 1 || report.Port > 65535 || report.Ordinal < 0 {
			return nil, false, apperr.ErrInvalidParam
		}
		bindHost, err := normalizeBindHost(report.BindHost)
		if err != nil {
			return nil, false, apperr.ErrInvalidParam
		}
		key := "primary"
		if !backend {
			key = net.JoinHostPort(bindHost, strconv.Itoa(report.Port))
		}
		if _, exists := seen[key]; exists {
			return nil, false, apperr.ErrInvalidParam
		}
		seen[key] = struct{}{}
		result = append(result, normalizedEndpointReport{key: key, bindHost: bindHost, port: report.Port, ordinal: report.Ordinal})
	}
	return result, true, nil
}

func normalizeBindHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "" {
		return "", nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	if !validHostname(host) {
		return "", apperr.ErrInvalidParam
	}
	return host, nil
}

func validHostname(host string) bool {
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if part == "" || len(part) > 63 || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
		for _, char := range part {
			if char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
				return false
			}
		}
	}
	return true
}

func parseAddress(raw string) (string, int, error) {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "/@?#") {
		return "", 0, apperr.ErrInvalidParam
	}
	host, portText, err := net.SplitHostPort(raw)
	if err != nil {
		return "", 0, err
	}
	normalizedHost, err := normalizeBindHost(host)
	if err != nil || normalizedHost == "" {
		return "", 0, apperr.ErrInvalidParam
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, apperr.ErrInvalidParam
	}
	return normalizedHost, port, nil
}

func refreshCompatibilityAddress(tx *gorm.DB, ident *model.AgentIdentity) ([]AgentEndpointView, error) {
	var endpoints []model.AgentEndpoint
	if err := tx.Where("agent_identity_id = ?", ident.ID).Order("ordinal ASC, endpoint_key ASC").Find(&endpoints).Error; err != nil {
		return nil, err
	}
	views := endpointViews(endpoints)
	ident.LastAddr = ""
	for _, endpoint := range endpoints {
		if endpoint.Active {
			ident.LastAddr = effectiveEndpointAddress(&endpoint)
			break
		}
	}
	if err := tx.Save(ident).Error; err != nil {
		return nil, err
	}
	return views, nil
}

func endpointViews(endpoints []model.AgentEndpoint) []AgentEndpointView {
	views := make([]AgentEndpointView, 0, len(endpoints))
	for i := range endpoints {
		endpoint := &endpoints[i]
		view := AgentEndpointView{
			EndpointKey: endpoint.EndpointKey, Ordinal: endpoint.Ordinal, ReportedBindHost: endpoint.ReportedBindHost,
			ReportedPort: endpoint.ReportedPort, DetectedAddress: endpoint.DetectedAddress,
			OverrideAddress: endpoint.OverrideAddress, EffectiveAddress: effectiveEndpointAddress(endpoint),
			Active: endpoint.Active, LastSeenAt: endpoint.LastSeenAt, Source: "detected",
		}
		if endpoint.OverrideAddress != nil {
			view.Source = "override"
		}
		views = append(views, view)
	}
	return views
}

func agentEndpointViews(db *gorm.DB, identityID uint) ([]AgentEndpointView, error) {
	var endpoints []model.AgentEndpoint
	if err := db.Where("agent_identity_id = ?", identityID).Order("active DESC, ordinal ASC, endpoint_key ASC").Find(&endpoints).Error; err != nil {
		return nil, err
	}
	return endpointViews(endpoints), nil
}

func effectiveEndpointAddress(endpoint *model.AgentEndpoint) string {
	if endpoint.OverrideAddress != nil {
		return *endpoint.OverrideAddress
	}
	return endpoint.DetectedAddress
}

// SetAgentEndpointOverride 设置或清除单个 endpoint 的可达地址覆盖。
func (s *V2ControlPlaneService) SetAgentEndpointOverride(identityID, endpointKey string, p AgentEndpointOverrideParams) (*AgentEndpointView, error) {
	if !validUUID(identityID) || strings.TrimSpace(endpointKey) == "" || strings.TrimSpace(p.Reason) == "" {
		return nil, apperr.ErrInvalidParam
	}
	if p.OverrideAddress != nil {
		normalized, port, err := parseAddress(*p.OverrideAddress)
		if err != nil {
			return nil, apperr.ErrInvalidParam
		}
		value := net.JoinHostPort(normalized, strconv.Itoa(port))
		p.OverrideAddress = &value
	}
	var out AgentEndpointView
	var namespace string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ident, err := findIdentityByID(tx, identityID)
		if err != nil {
			return err
		}
		if ident == nil {
			return apperr.ErrInstanceNotFound
		}
		var endpoint model.AgentEndpoint
		if err := tx.Where("agent_identity_id = ? AND endpoint_key = ?", ident.ID, endpointKey).First(&endpoint).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrInstanceNotFound
			}
			return err
		}
		if !endpoint.Active && p.OverrideAddress != nil {
			return apperr.ErrIllegalState
		}
		old := endpoint.OverrideAddress
		endpoint.OverrideAddress = p.OverrideAddress
		if err := tx.Save(&endpoint).Error; err != nil {
			return err
		}
		views, err := refreshCompatibilityAddress(tx, ident)
		if err != nil {
			return err
		}
		for _, view := range views {
			if view.EndpointKey == endpoint.EndpointKey {
				out = view
				break
			}
		}
		var ns model.Namespace
		if err := tx.First(&ns, ident.NamespaceID).Error; err != nil {
			return err
		}
		namespace = ns.Code
		return createAudit(tx, model.AuditLog{
			NamespaceCode: ns.Code, Operator: operatorOrSystem(p.Operator), Action: "identity.endpoint_override_changed",
			TargetType: model.TargetTypeInstance, TargetRef: ident.IdentityID, Result: model.ResultOK, ClientIP: p.ClientIP,
			Detail: endpointOverrideAuditDetail(endpoint.EndpointKey, optionalIdentityServerID(ident), p.TraceID, old, endpoint.OverrideAddress, p.Reason),
		})
	})
	if err != nil {
		return nil, err
	}
	if s.notifier != nil {
		s.notifier.NotifyTopologyChange(namespace)
	}
	return &out, nil
}

func endpointOverrideAuditDetail(key string, serverID *string, traceID string, old, next *string, reason string) string {
	oldValue, nextValue := "", ""
	if old != nil {
		oldValue = *old
	}
	if next != nil {
		nextValue = *next
	}
	encoded, _ := json.Marshal(struct {
		EndpointKey string  `json:"endpointKey"`
		ServerID    *string `json:"serverId"`
		TraceID     string  `json:"traceId"`
		Old         string  `json:"old"`
		New         string  `json:"new"`
		Reason      string  `json:"reason"`
	}{
		EndpointKey: key,
		ServerID:    serverID,
		TraceID:     traceID,
		Old:         endpointAddressSummary(oldValue),
		New:         endpointAddressSummary(nextValue),
		Reason:      reason,
	})
	return string(encoded)
}

func endpointAddressSummary(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:6])
}
