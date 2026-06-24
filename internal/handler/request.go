package handler

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
)

// 批量操作动作（FR-74）：配置项 / 文件对象批量端点共用。
const (
	batchActionDelete  = "delete"
	batchActionDisable = "disable"
	batchActionEnable  = "enable"
)

// batchRequest 是批量端点的统一请求体（FR-74）：动作 + 目标 id 集合。
type batchRequest struct {
	Action string `json:"action"`
	IDs    []uint `json:"ids"`
}

// decodeBatchRequest 解析并校验批量请求体：非法 JSON / 非法 action / 空 ids 一律 INVALID_PARAM（400）。
func decodeBatchRequest(r *http.Request) (batchRequest, error) {
	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return batchRequest{}, apperr.ErrInvalidParam
	}
	switch req.Action {
	case batchActionDelete, batchActionDisable, batchActionEnable:
	default:
		return batchRequest{}, apperr.ErrInvalidParam
	}
	if len(req.IDs) == 0 {
		return batchRequest{}, apperr.ErrInvalidParam
	}
	return req, nil
}

// longpollSettingMaxHoldMs 是长轮询挂起上限设置 key（与 service.SettingsService 同字面值，FR-61）。
const longpollSettingMaxHoldMs = "longpoll.max-hold-ms"

// longpollSettings 是长轮询对运维设置的窄读依赖（由 service.SettingsService 实现，FR-61）。
// 各挂起点每请求读 longpoll.max-hold-ms 即热生效，不再启动期固定。
type longpollSettings interface {
	GetInt(key string) int
}

// resolveHoldTimeout 解析本次长轮询挂起时长：取 min(客户端 timeoutMs, 服务端上限)。
// 服务端上限从设置 store 读（热生效）；客户端 timeoutMs 为空 / 非法 / 非正则忽略，按服务端上限。
func resolveHoldTimeout(settings longpollSettings, clientTimeoutMs string) time.Duration {
	timeout := time.Duration(settings.GetInt(longpollSettingMaxHoldMs)) * time.Millisecond
	if v, err := strconv.Atoi(clientTimeoutMs); err == nil && v > 0 {
		if d := time.Duration(v) * time.Millisecond; d < timeout {
			timeout = d
		}
	}
	return timeout
}

// clientIP 提取请求来源 IP：优先 X-Forwarded-For 首跳、其次 X-Real-IP，再退回 RemoteAddr 的 host。
// 供审计 client_ip 落库（内网控制面，信任前置代理透传的来源头；无代理时即 TCP 对端 IP）。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// 多级代理时取第一跳（最初客户端）
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
