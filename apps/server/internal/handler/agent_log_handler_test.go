package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// newAgentLogTestSvc 构造内存 sqlite 的日志服务（含读路径反查所需的 namespace / server 表）。
func newAgentLogTestSvc(t *testing.T) (*service.AgentLogService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}, &model.AuditLog{}, &model.Namespace{}, &model.Server{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"agent_command", "audit_log", "server", "namespace"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	svc := service.NewAgentLogService(db, repository.NewAgentCommandRepository(db), repository.NewAuditLogRepository(db))
	return svc, db
}

// seedAgentLogScopeServer 写入一条 (namespace, serverId) 归属，供读路径按 serverId 反查命名空间。
func seedAgentLogScopeServer(t *testing.T, db *gorm.DB, namespaceCode, serverID string) {
	t.Helper()
	var namespace model.Namespace
	if err := db.Where("code = ?", namespaceCode).First(&namespace).Error; err != nil {
		namespace = model.Namespace{Code: namespaceCode, Name: namespaceCode}
		if err := db.Create(&namespace).Error; err != nil {
			t.Fatalf("创建 namespace %s 失败: %v", namespaceCode, err)
		}
	}
	if err := db.Create(&model.Server{NamespaceID: namespace.ID, ServerID: serverID, Kind: "backend"}).Error; err != nil {
		t.Fatalf("创建 server %s/%s 失败: %v", namespaceCode, serverID, err)
	}
}

// doAgentLogConsume 以指定 serverId 路由参数与操作人上下文调用读指定命令日志端点。
func doAgentLogConsume(t *testing.T, h *AgentLogHandler, commandID uint, query, serverID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"commandId": commandID})
	r := httptest.NewRequest(http.MethodPost, "/admin/v1/instances/"+serverID+"/logs/grants/legacy/consume"+query, bytes.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("serverId", serverID)
	r = r.WithContext(context.WithValue(auth.WithOperator(r.Context(), "alice"), chi.RouteCtxKey, routeContext))
	w := httptest.NewRecorder()
	h.ConsumeApproved(w, r)
	return w
}

// TestAgentLogReadCommandRejectsCrossNamespaceRead 读指定命令日志必须限定 namespace：
// 同名 serverId 跨环境共存时缺省 namespace 无法消歧，一律按不存在拒绝。
func TestAgentLogReadCommandRejectsCrossNamespaceRead(t *testing.T) {
	svc, db := newAgentLogTestSvc(t)
	seedAgentLogScopeServer(t, db, "prod", "log-1")
	seedAgentLogScopeServer(t, db, "test", "log-1")
	h := NewAgentLogHandler(svc, nil)
	cmd := &model.AgentCommand{
		NamespaceCode: "test", ServerID: "log-1", Type: model.CommandTypeTailLogs,
		Status: model.CommandStatusDone, LogContent: `[{"level":"info","text":"test 环境私有日志"}]`, Operator: "alice",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建日志命令失败: %v", err)
	}

	if w := doAgentLogConsume(t, h, cmd.ID, "?namespace=prod", "log-1"); w.Code != http.StatusNotFound {
		t.Fatalf("跨 namespace 读取应按不存在拒绝，实际 %d：%s", w.Code, w.Body.String())
	}
	if w := doAgentLogConsume(t, h, cmd.ID, "", "log-1"); w.Code != http.StatusNotFound {
		t.Fatalf("同名 serverId 跨环境歧义应拒绝，实际 %d：%s", w.Code, w.Body.String())
	}
	w := doAgentLogConsume(t, h, cmd.ID, "?namespace=test", "log-1")
	if w.Code != http.StatusOK {
		t.Fatalf("正确 namespace 应返回日志，实际 %d：%s", w.Code, w.Body.String())
	}
}

// TestAgentLogReadCommandWithoutNamespaceUsesUniqueScope 未带 namespace 的旧调用方在归属唯一时仍可读。
func TestAgentLogReadCommandWithoutNamespaceUsesUniqueScope(t *testing.T) {
	svc, db := newAgentLogTestSvc(t)
	seedAgentLogScopeServer(t, db, "prod", "log-7")
	h := NewAgentLogHandler(svc, nil)
	cmd := &model.AgentCommand{
		NamespaceCode: "prod", ServerID: "log-7", Type: model.CommandTypeTailLogs,
		Status: model.CommandStatusDone, LogContent: `[{"level":"info","text":"生产日志"}]`, Operator: "alice",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建日志命令失败: %v", err)
	}
	if w := doAgentLogConsume(t, h, cmd.ID, "", "log-7"); w.Code != http.StatusOK {
		t.Fatalf("归属唯一时应按 serverId 反查成功，实际 %d：%s", w.Code, w.Body.String())
	}
}
