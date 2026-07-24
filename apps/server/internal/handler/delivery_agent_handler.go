package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// DeliveryAgentHandler 处理交付 agent 面端点（FR-165，spec §5.2）：命令经既有长轮询通道下发，
// 此处为配套的清单拉取 / 阶段回执接口。挂 agent 双 header 鉴权中间件注入权威身份，服务层按身份做归属校验。
type DeliveryAgentHandler struct {
	svc *service.DeliveryBlobService
}

// NewDeliveryAgentHandler 构造交付 agent 面 handler。
func NewDeliveryAgentHandler(svc *service.DeliveryBlobService) *DeliveryAgentHandler {
	return &DeliveryAgentHandler{svc: svc}
}

// UploadManifest 处理 GET .../orders/{id}/upload-manifest：模板源拉待上传 blob 清单（path/sha256/size）。
func (h *DeliveryAgentHandler) UploadManifest(w http.ResponseWriter, r *http.Request) {
	id, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	orderID, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.UploadManifest(id, orderID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Manifest 处理 GET .../orders/{id}/manifest：目标拉本服差异清单与配置项摘要。
func (h *DeliveryAgentHandler) Manifest(w http.ResponseWriter, r *http.Request) {
	id, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	orderID, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.TargetManifest(id, orderID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// deliveryResultRequest 是阶段回执请求体（spec §5.2）。
type deliveryResultRequest struct {
	Phase            string `json:"phase"`
	Status           string `json:"status"`
	ChangedFileCount int    `json:"changedFileCount"`
	SkippedFileCount int    `json:"skippedFileCount"`
	BackupPresent    bool   `json:"backupPresent"`
	Error            string `json:"error"`
}

// Result 处理 POST .../orders/{id}/result：阶段回执（upload / push / activate / rollback）。
func (h *DeliveryAgentHandler) Result(w http.ResponseWriter, r *http.Request) {
	id, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	orderID, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req deliveryResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	err := h.svc.ReceiveResult(id, orderID, service.DeliveryResultInput{
		Phase:            req.Phase,
		Status:           req.Status,
		ChangedFileCount: req.ChangedFileCount,
		SkippedFileCount: req.SkippedFileCount,
		BackupPresent:    req.BackupPresent,
		Error:            req.Error,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
