package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

func TestFR204ProxyEndpointsUseDetectedHostAndKeepOverride(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "20400000-0000-4000-8000-000000000001"
	_, err = svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindProxy, BootID: "boot-204",
		Addr: "伪造.example:25577", DetectedHost: "203.0.113.10",
		ListenersProvided: true,
		Listeners: []AgentEndpointReport{
			{BindHost: "0.0.0.0", Port: 25577, Ordinal: 0},
			{BindHost: "::", Port: 25578, Ordinal: 1},
		},
	})
	if err != nil {
		t.Fatalf("注册双 listener proxy 失败: %v", err)
	}
	var identity model.AgentIdentity
	if err := db.Where("identity_id = ?", identityID).First(&identity).Error; err != nil {
		t.Fatalf("读取身份失败: %v", err)
	}
	var endpoints []model.AgentEndpoint
	if err := db.Where("agent_identity_id = ?", identity.ID).Order("ordinal").Find(&endpoints).Error; err != nil {
		t.Fatalf("读取 endpoint 失败: %v", err)
	}
	if len(endpoints) != 2 || endpoints[0].DetectedAddress != "203.0.113.10:25577" || endpoints[0].ReportedBindHost != "0.0.0.0" {
		t.Fatalf("endpoint 必须以 TCP 对端 + 上报端口生成，实际 %+v", endpoints)
	}
	if endpoints[0].DetectedAddress == "伪造.example:25577" {
		t.Fatal("请求体 addr 不得成为探测地址")
	}
	override := "proxy.example.com:25577"
	if _, err := svc.SetAgentEndpointOverride(identityID, endpoints[0].EndpointKey, AgentEndpointOverrideParams{
		OverrideAddress: &override, Reason: "公网 NAT 映射", Operator: "admin", TraceID: "service-trace-204",
	}); err != nil {
		t.Fatalf("设置 active endpoint 覆盖失败: %v", err)
	}
	var audit model.AuditLog
	if err := db.Where("action = ?", "identity.endpoint_override_changed").First(&audit).Error; err != nil {
		t.Fatalf("读取 endpoint 覆盖审计失败: %v", err)
	}
	if audit.Detail == "" || strings.Contains(audit.Detail, override) {
		t.Fatalf("审计只能保存覆盖地址摘要，不能回显原地址，实际 %s", audit.Detail)
	}
	var detail struct {
		ServerID *string `json:"serverId"`
		TraceID  string  `json:"traceId"`
	}
	if err := json.Unmarshal([]byte(audit.Detail), &detail); err != nil {
		t.Fatalf("解析 endpoint 覆盖审计详情失败: %v", err)
	}
	if detail.ServerID != nil || detail.TraceID != "service-trace-204" {
		t.Fatalf("审计必须显式记录操作时未绑定 serverId 与 traceId，实际 %+v", detail)
	}
	_, err = svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindProxy, BootID: "boot-204-next",
		DetectedHost: "203.0.113.11", ListenersProvided: true, Listeners: []AgentEndpointReport{{BindHost: "0.0.0.0", Port: 25577, Ordinal: 0}},
	})
	if err != nil {
		t.Fatalf("重注册对账失败: %v", err)
	}
	if err := db.Where("identity_id = ?", identityID).First(&identity).Error; err != nil {
		t.Fatalf("读取身份失败: %v", err)
	}
	if identity.LastAddr != "proxy.example.com:25577" {
		t.Fatalf("兼容地址必须投影首个 active endpoint 的覆盖地址，实际 %q", identity.LastAddr)
	}
	if err := db.Where("agent_identity_id = ? AND endpoint_key = ?", identity.ID, endpoints[0].EndpointKey).First(&endpoints[0]).Error; err != nil {
		t.Fatalf("读取重现 endpoint 失败: %v", err)
	}
	if endpoints[0].OverrideAddress == nil || *endpoints[0].OverrideAddress != "proxy.example.com:25577" || endpoints[0].DetectedAddress != "203.0.113.11:25577" {
		t.Fatalf("重现 endpoint 必须保留覆盖并刷新探测地址，实际 %+v", endpoints[0])
	}
	var inactive model.AgentEndpoint
	if err := db.Where("agent_identity_id = ? AND endpoint_key = ?", identity.ID, endpoints[1].EndpointKey).First(&inactive).Error; err != nil {
		t.Fatalf("读取缺失 listener 失败: %v", err)
	}
	if inactive.Active {
		t.Fatal("本轮缺失 listener 必须标记 inactive")
	}
	inactiveOverride := "other.example.com:25578"
	if _, err := svc.SetAgentEndpointOverride(identityID, inactive.EndpointKey, AgentEndpointOverrideParams{
		OverrideAddress: &inactiveOverride, Reason: "错误操作", Operator: "admin",
	}); !errors.Is(err, apperr.ErrIllegalState) {
		t.Fatalf("inactive endpoint 不得新增覆盖，实际 %v", err)
	}
}

func TestFR204EndpointReportRejectsRoleMismatchAndDuplicate(t *testing.T) {
	_, svc := newV2ControlPlaneTestService(t)
	_, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	_, err = svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "20400000-0000-4000-8000-000000000002", Kind: model.ServerKindBackend, BootID: "boot-204-backend",
		DetectedHost: "203.0.113.12", ListenersProvided: true, Listeners: []AgentEndpointReport{{BindHost: "0.0.0.0", Port: 25565, Ordinal: 0}},
	})
	if !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("backend 不得上报 listeners，实际 %v", err)
	}
	_, err = svc.RegisterAgentV2(AgentRegisterV2Params{
		Token: token, IdentityID: "20400000-0000-4000-8000-000000000003", Kind: model.ServerKindProxy, BootID: "boot-204-proxy",
		DetectedHost: "203.0.113.12", ListenersProvided: true, Listeners: []AgentEndpointReport{
			{BindHost: " 0.0.0.0 ", Port: 25577, Ordinal: 0}, {BindHost: "0.0.0.0", Port: 25577, Ordinal: 1},
		},
	})
	if !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("proxy 重复规范化 listener 必须拒绝，实际 %v", err)
	}
}
