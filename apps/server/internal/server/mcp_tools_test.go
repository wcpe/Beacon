package server

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

func TestMCPToolCoverageFailsClosedForForbiddenOrGenericProxyTools(t *testing.T) {
	for _, profile := range []string{model.MCPClientProfileObserver, model.MCPClientProfileAutomation} {
		for _, name := range MCPToolNames(profile) {
			if name == "" || name == "beacon.approvals.approve" || name == "beacon.approvals.reject" || name == "beacon.permit.create" || name == "beacon.http.request" || name == "beacon.sql.query" || name == "beacon.files.proxy" || name == "beacon.internal.call" {
				t.Fatalf("MCP 工具目录含禁止或通用代理工具: profile=%s tool=%s", profile, name)
			}
		}
	}
}

func TestMCPToolCoverageObserverCannotWithdrawAndAutomationCanOnlyWithdrawOwn(t *testing.T) {
	observer := MCPToolNames(model.MCPClientProfileObserver)
	automation := MCPToolNames(model.MCPClientProfileAutomation)
	if containsMCPTool(observer, "beacon.approvals.own.withdraw") || !containsMCPTool(automation, "beacon.approvals.own.withdraw") {
		t.Fatalf("审批撤回工具的 profile 覆盖不符: observer=%v automation=%v", observer, automation)
	}
	if containsMCPTool(automation, "beacon.approvals.approve") || containsMCPTool(automation, "beacon.approvals.reject") {
		t.Fatalf("机器主体不得发现审批决定工具: %v", automation)
	}
}

func TestMCPToolCoverageFileAndOverrideChangesAreAutomationOnly(t *testing.T) {
	observer := MCPToolNames(model.MCPClientProfileObserver)
	automation := MCPToolNames(model.MCPClientProfileAutomation)
	for _, name := range []string{
		"beacon.config.delete",
		"beacon.config.batch.delete",
		"beacon.config.batch.enable",
		"beacon.config.batch.disable",
		"beacon.files.create",
		"beacon.files.import",
		"beacon.files.publish",
		"beacon.files.rollback",
		"beacon.files.delete",
		"beacon.files.batch.delete",
		"beacon.files.batch.enable",
		"beacon.files.batch.disable",
		"beacon.assets.preview.request",
		"beacon.assets.preview.consume",
		"beacon.messages.payload.request",
		"beacon.messages.payload.consume",
		"beacon.override-sets.publish",
		"beacon.override-sets.rollback",
		"beacon.override-sets.delete",
	} {
		if containsMCPTool(observer, name) || !containsMCPTool(automation, name) {
			t.Fatalf("文件与覆盖集审批工具的 profile 覆盖不符: tool=%s observer=%v automation=%v", name, observer, automation)
		}
	}
}

func TestMCPToolCoverageDeliverySubmitAndDraftDeleteAreAutomationOnly(t *testing.T) {
	observer := MCPToolNames(model.MCPClientProfileObserver)
	automation := MCPToolNames(model.MCPClientProfileAutomation)
	for _, name := range []string{"beacon.delivery.order.submit", "beacon.delivery.order.delete"} {
		if containsMCPTool(observer, name) || !containsMCPTool(automation, name) {
			t.Fatalf("交付审批工具 profile 覆盖不符: tool=%s observer=%v automation=%v", name, observer, automation)
		}
	}
}

func TestMCPToolCoverageLifecycleRequestsAreAutomationOnly(t *testing.T) {
	observer := MCPToolNames(model.MCPClientProfileObserver)
	automation := MCPToolNames(model.MCPClientProfileAutomation)
	for _, name := range []string{
		"beacon.lifecycle.namespace.archive",
		"beacon.lifecycle.namespace.restore",
		"beacon.lifecycle.namespace.permanent-delete",
		"beacon.lifecycle.server.archive",
		"beacon.lifecycle.server.restore",
		"beacon.lifecycle.server.permanent-delete",
	} {
		if containsMCPTool(observer, name) || !containsMCPTool(automation, name) {
			t.Fatalf("生命周期审批工具的 profile 覆盖不符: tool=%s observer=%v automation=%v", name, observer, automation)
		}
	}
}

func containsMCPTool(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}
