package service

import "sync"

// 文件同步管理台 SSE 事件类型。
const (
	FileSyncEventTypeTask   = "task"
	FileSyncEventTypeTarget = "target"
	FileSyncEventTypeLog    = "log"
)

// FileSyncEvent 是管理台 SSE 的轻量事件。
type FileSyncEvent struct {
	Type      string               `json:"type"`
	TaskID    uint                 `json:"taskId"`
	Status    string               `json:"status,omitempty"`
	LogID     uint                 `json:"logId,omitempty"`
	BatchID   uint                 `json:"batchId,omitempty"`
	ServerID  string               `json:"serverId,omitempty"`
	Level     string               `json:"level,omitempty"`
	Message   string               `json:"message,omitempty"`
	Target    *FileSyncTargetEvent `json:"target,omitempty"`
	CreatedAt string               `json:"createdAt,omitempty"`
}

// FileSyncTargetEvent 是单台目标的 SSE 补丁。
type FileSyncTargetEvent struct {
	TaskID           string `json:"taskId"`
	BatchID          uint   `json:"batchId"`
	BatchNo          int    `json:"batchNo"`
	ServerID         string `json:"serverId"`
	Status           string `json:"status"`
	BackupPath       string `json:"backupPath"`
	CurrentFileCount int    `json:"currentFileCount"`
	ChangedFileCount int    `json:"changedFileCount"`
	SkippedFileCount int    `json:"skippedFileCount"`
	BytesTotal       int64  `json:"bytesTotal"`
	BytesDone        int64  `json:"bytesDone"`
	Error            string `json:"error"`
	UpdatedAt        string `json:"updatedAt"`
}

// FileSyncEventHub 是文件同步管理台 SSE 的进程内唤醒器。
// 真源仍是 DB 日志和任务状态；hub 只负责连接在线时即时推送。
type FileSyncEventHub struct {
	mu      sync.Mutex
	waiters map[uint]map[chan FileSyncEvent]struct{}
}

// NewFileSyncEventHub 构造空 hub。
func NewFileSyncEventHub() *FileSyncEventHub {
	return &FileSyncEventHub{waiters: map[uint]map[chan FileSyncEvent]struct{}{}}
}

// Register 注册某任务的事件订阅。
func (h *FileSyncEventHub) Register(taskID uint) chan FileSyncEvent {
	ch := make(chan FileSyncEvent, 16)
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.waiters[taskID]
	if set == nil {
		set = map[chan FileSyncEvent]struct{}{}
		h.waiters[taskID] = set
	}
	set[ch] = struct{}{}
	return ch
}

// Deregister 移除订阅。
func (h *FileSyncEventHub) Deregister(taskID uint, ch chan FileSyncEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.waiters[taskID]
	if set == nil {
		return
	}
	delete(set, ch)
	if len(set) == 0 {
		delete(h.waiters, taskID)
	}
}

// Publish 非阻塞推送一条事件。
func (h *FileSyncEventHub) Publish(taskID uint, evt FileSyncEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.waiters[taskID] {
		select {
		case ch <- evt:
		default:
		}
	}
}
