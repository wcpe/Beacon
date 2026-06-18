package service

import (
	"context"
	"time"

	"beacon/internal/runtime/longpoll"
	"beacon/internal/sse"
)

// ChannelMD5 是某 agent 三条 server→agent 推送通道的当前指纹快照。
// 通道与原长轮询一一对应：配置（通道A）、文件树（通道B）、三方覆盖集（FR-15）。
type ChannelMD5 struct {
	Config   string
	File     string
	Override string
}

// StreamService 编排 server→agent 单条 SSE 推送（取代 ADR-0006 的三条长轮询）。
//
// 它复用三个 EffectiveService 计算各通道当前 md5（"唤醒即重算比对"口径不变），
// 复用配置/文件两个 Hub 的唤醒集合做定向直播（override 与 file 同属通道B，共用 fileHub）。
// 流只发"变更通知"，agent 收到后用现有 HTTP 端点取内容并应用（见 ADR-0015）。
type StreamService struct {
	effSvc     *EffectiveService
	fileEffSvc *FileEffectiveService
	ovrEffSvc  *OverrideEffectiveService
	configHub  *longpoll.Hub
	fileHub    *longpoll.Hub
	pingEvery  time.Duration
}

// NewStreamService 构造推送编排器。
// configHub 唤醒配置通道、fileHub 唤醒文件/覆盖通道（与长轮询同源，互不触发无谓重算）。
// pingEvery 为无变更时的保活间隔（避免反代按读空闲断流；<=0 关闭保活）。
func NewStreamService(
	effSvc *EffectiveService,
	fileEffSvc *FileEffectiveService,
	ovrEffSvc *OverrideEffectiveService,
	configHub, fileHub *longpoll.Hub,
	pingEvery time.Duration,
) *StreamService {
	return &StreamService{
		effSvc: effSvc, fileEffSvc: fileEffSvc, ovrEffSvc: ovrEffSvc,
		configHub: configHub, fileHub: fileHub, pingEvery: pingEvery,
	}
}

// EventSink 是 SSE 写出口：handler 实现它把事件写到 http.ResponseWriter 并 flush。
// 写失败（客户端断连）返回 error，编排循环据此退出。
type EventSink interface {
	Send(e sse.Event) error
}

// currentMD5 计算某 agent 三条通道的当前有效 md5（各自独立 Resolve）。
func (s *StreamService) currentMD5(ns, serverID, groupHint string) (ChannelMD5, error) {
	eff, err := s.effSvc.Resolve(ns, serverID, groupHint)
	if err != nil {
		return ChannelMD5{}, err
	}
	tree, err := s.fileEffSvc.Resolve(ns, serverID, groupHint)
	if err != nil {
		return ChannelMD5{}, err
	}
	ovr, err := s.ovrEffSvc.Resolve(ns, serverID, groupHint)
	if err != nil {
		return ChannelMD5{}, err
	}
	return ChannelMD5{Config: eff.MD5, File: tree.FileTreeMD5, Override: ovr.OverrideMD5}, nil
}

// DiffEvents 对账：把 agent 上报的各通道 md5 与当前 md5 比对，返回落后通道应补发的 *-changed 事件。
// 纯函数（不碰 IO），便于穷举单测"连接即对账、不丢更新"的正确性。
func DiffEvents(reported, current ChannelMD5) []sse.Event {
	events := make([]sse.Event, 0, 3)
	if current.Config != reported.Config {
		events = append(events, sse.Event{Type: sse.EventConfigChanged, MD5: current.Config})
	}
	if current.File != reported.File {
		events = append(events, sse.Event{Type: sse.EventFileChanged, MD5: current.File})
	}
	if current.Override != reported.Override {
		events = append(events, sse.Event{Type: sse.EventOverrideChanged, MD5: current.Override})
	}
	return events
}

// Run 驱动一条 SSE 连接的完整生命周期：先注册 waiter（消除注册前发布丢唤醒窗口），
// 再连接即对账补发落下的增量、发 ready，最后转直播——被唤醒即重算比对、真变才发通知。
//
// 同步阻塞直到 ctx 取消（客户端断连/服务关停）或写出失败；调用方应在独立 goroutine（每连接一条）里跑。
func (s *StreamService) Run(ctx context.Context, ns, serverID, groupHint string, reported ChannelMD5, sink EventSink) error {
	// 先注册两个 Hub 的 waiter：配置通道 + 文件/覆盖通道（与长轮询"先注册后算"同序，杜绝注册前发布丢唤醒）。
	cfgWaiter := s.configHub.Register(ns, serverID)
	defer s.configHub.Deregister(cfgWaiter)
	fileWaiter := s.fileHub.Register(ns, serverID)
	defer s.fileHub.Deregister(fileWaiter)

	// 已发往 agent 的各通道 md5：对账与直播都据此判"真变才发"，避免重复通知。
	sent := reported
	if err := s.reconcileAndSend(ns, serverID, groupHint, &sent, sink); err != nil {
		return err
	}
	// 首轮对账补发完，发 ready 标记，转入直播。
	if err := sink.Send(sse.Event{Type: sse.EventReady}); err != nil {
		return err
	}

	// 直播：任一 Hub 唤醒（或保活到点）即重算比对，真变才发通知。
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-cfgWaiter.NotifyChan():
			if err := s.reconcileAndSend(ns, serverID, groupHint, &sent, sink); err != nil {
				return err
			}
		case <-fileWaiter.NotifyChan():
			if err := s.reconcileAndSend(ns, serverID, groupHint, &sent, sink); err != nil {
				return err
			}
		case <-pingTimer(s.pingEvery):
			// 保活：发一条 SSE 注释行（: 开头），不触发 agent 任何取数据；写失败即客户端断连。
			if err := sink.Send(sse.Event{Type: sse.EventPing}); err != nil {
				return err
			}
		}
	}
}

// reconcileAndSend 重算当前 md5、与已发 md5 比对，对落后通道发 *-changed 并推进 sent。
func (s *StreamService) reconcileAndSend(ns, serverID, groupHint string, sent *ChannelMD5, sink EventSink) error {
	current, err := s.currentMD5(ns, serverID, groupHint)
	if err != nil {
		return err
	}
	for _, e := range DiffEvents(*sent, current) {
		if err := sink.Send(e); err != nil {
			return err
		}
	}
	*sent = current
	return nil
}

// pingTimer 返回一个到点触发的通道；保活关闭（<=0）时返回永不触发的 nil 通道。
func pingTimer(d time.Duration) <-chan time.Time {
	if d <= 0 {
		return nil
	}
	return time.After(d)
}
