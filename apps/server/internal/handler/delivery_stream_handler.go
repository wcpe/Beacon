package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// DeliveryStreamHandler 处理交付流式数据面（FR-165，spec §5.3）：sha256 内容寻址 blob 的
// HEAD 就绪查询 / PUT 流式上传 / GET 流式下载（Range 断点）。挂 agent 双 header 鉴权中间件注入
// 权威身份，再由服务层做 blob 归属校验（模板源仅上传本单待传 sha、目标仅下载本单清单内 sha）。
type DeliveryStreamHandler struct {
	svc *service.DeliveryBlobService
}

// NewDeliveryStreamHandler 构造流式数据面 handler。
func NewDeliveryStreamHandler(svc *service.DeliveryBlobService) *DeliveryStreamHandler {
	return &DeliveryStreamHandler{svc: svc}
}

// Head 处理 HEAD /beacon/v2/stream/delivery/blobs/{sha256}：就绪查询（去重 / 断点判断）。
// ready 返回 200 + Content-Length；未就绪 / 不存在 404（uploading 视同不存在）。
func (h *DeliveryStreamHandler) Head(w http.ResponseWriter, r *http.Request) {
	id, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	sha := chi.URLParam(r, "sha256")
	if err := h.svc.AuthorizeBlobHead(id, sha); err != nil {
		render.WriteError(w, r, err)
		return
	}
	size, err := h.svc.Head(sha)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
}

// Upload 处理 PUT /beacon/v2/stream/delivery/blobs/{sha256}：模板源流式上传。
// Content-Length 必填（411）；服务端边收边算 sha256，不符声明 422 丢弃；一致原子入位 204。
func (h *DeliveryStreamHandler) Upload(w http.ResponseWriter, r *http.Request) {
	id, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	sha := chi.URLParam(r, "sha256")
	if err := h.svc.AuthorizeBlobUpload(id, sha); err != nil {
		render.WriteError(w, r, err)
		return
	}
	if r.ContentLength < 0 {
		render.WriteError(w, r, apperr.ErrDeliveryLengthRequired)
		return
	}
	if err := h.svc.Store(sha, r.ContentLength, r.Body); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Download 处理 GET /beacon/v2/stream/delivery/blobs/{sha256}：目标流式下载，支持 Range 断点续传。
// 用 http.ServeContent 处理 Range / Content-Length / If-Range；modtime 置零不发 Last-Modified。
func (h *DeliveryStreamHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	sha := chi.URLParam(r, "sha256")
	if err := h.svc.AuthorizeBlobDownload(id, sha); err != nil {
		render.WriteError(w, r, err)
		return
	}
	file, _, release, err := h.svc.Open(sha)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	defer release()    // 归还下载并发额度（LIFO 后执行）
	defer file.Close() // 先关文件句柄再归还额度（Open 的 release 只管额度、不关 file）
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, file)
}
