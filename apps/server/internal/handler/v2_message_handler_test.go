package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// msgTrustAllowAll 测试用信任判定：全放行（本文件不测跨域路径）。
type msgTrustAllowAll struct{}

func (msgTrustAllowAll) NamespaceTrustAllowed(_, _ uint, _ string) bool { return true }

// newMsgHandlerForTest 构造挂真实服务的消息处理器：中转不接落库通道（wire 测试不碰 DB），
// 健康视图直填供广播寻址。
func newMsgHandlerForTest(views []healthview.View) *V2MessageHandler {
	viewStore := healthview.NewStore()
	viewStore.ReplaceAll(views)
	relay := service.NewMessageRelay(nil)
	svc := service.NewMessageService(relay, roster.NewStore(), msgTrustAllowAll{}, viewStore)
	return NewV2MessageHandler(svc)
}

// msgAgentRequest 构造带已鉴权身份 context 的请求（模拟 agent v2 鉴权中间件注入）。
func msgAgentRequest(serverID string, body any) *http.Request {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/beacon/v2/agent/messages/x", &buf)
	id := agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: serverID, Kind: "backend"}
	return req.WithContext(agentauth.WithIdentity(req.Context(), id))
}

// TestMsgHandlerStructuredPayloadRoundTrip 校验任意 JSON payload 经 send / poll 往返保持类型与内容。
func TestMsgHandlerStructuredPayloadRoundTrip(t *testing.T) {
	nowMs := time.Now().UTC().UnixMilli()
	tests := []struct {
		name    string
		suffix  string
		payload any
	}{
		{
			name:   "嵌套对象",
			suffix: "o1",
			payload: map[string]any{
				"servers": []any{"game-1", "game-2"},
				"online":  true,
				"count":   2,
				"meta":    map[string]any{"region": nil},
			},
		},
		{name: "顶层数组", suffix: "a1", payload: []any{map[string]any{"id": "game-1"}, false}},
		{name: "顶层数字", suffix: "d1", payload: 42},
		{name: "顶层布尔", suffix: "b1", payload: true},
		{name: "转义字符串", suffix: "s1", payload: "引号\"反斜杠\\换行\n"},
		{name: "空字符串", suffix: "e1", payload: ""},
		{name: "空值", suffix: "n1", payload: nil},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newMsgHandlerForTest(nil)
			rec := httptest.NewRecorder()
			h.Send(rec, msgAgentRequest("game-1", map[string]any{
				"messageId": uuidV7AtHandler(nowMs+int64(i), tc.suffix),
				"msgType":   "lodestone:roster", "targetKind": "server",
				"targetServerId": "game-2", "payload": tc.payload,
			}))
			if rec.Code != http.StatusOK {
				t.Fatalf("结构化 payload send 应 200，实际 %d：%s", rec.Code, rec.Body.String())
			}

			pollRec := httptest.NewRecorder()
			h.Poll(pollRec, msgAgentRequest("game-2", map[string]any{"waitSec": 0, "max": 1}))
			var pollResp struct {
				Messages []struct {
					MsgType string `json:"msgType"`
					Payload any    `json:"payload"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(pollRec.Body.Bytes(), &pollResp); err != nil || len(pollResp.Messages) != 1 {
				t.Fatalf("poll 应取到 1 条消息，实际 %s err=%v", pollRec.Body.String(), err)
			}
			var want any
			raw, _ := json.Marshal(tc.payload)
			_ = json.Unmarshal(raw, &want)
			got := pollResp.Messages[0]
			if got.MsgType != "lodestone:roster" || !reflect.DeepEqual(got.Payload, want) {
				t.Fatalf("payload 类型或内容未保持，got=%#v want=%#v", got, want)
			}
		})
	}
}

// TestMsgHandlerSendBroadcastAndPollFlag 校验广播 wire：send 接受 targetKind=broadcast + targetZone、
// poll 下发消息体带 broadcast:true（定向消息不带该键）。
func TestMsgHandlerSendBroadcastAndPollFlag(t *testing.T) {
	h := newMsgHandlerForTest([]healthview.View{
		{NamespaceID: 1, Namespace: "prod", ServerID: "game-1", Kind: "backend", ZoneName: "z1"},
		{NamespaceID: 1, Namespace: "prod", ServerID: "game-2", Kind: "backend", ZoneName: "z1"},
	})
	nowMs := time.Now().UTC().UnixMilli()

	// ① 广播 send（带 targetZone）：200 accepted。
	rec := httptest.NewRecorder()
	h.Send(rec, msgAgentRequest("game-1", map[string]any{
		"messageId": uuidV7AtHandler(nowMs, "b1"), "msgType": "announce",
		"targetKind": "broadcast", "targetZone": "z1", "payload": map[string]any{"text": "hello"},
	}))

	if rec.Code != http.StatusOK {
		t.Fatalf("广播 send 应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var sendResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &sendResp)
	if sendResp["status"] != "accepted" {
		t.Fatalf("有在线目标的广播应 accepted，实际 %v", sendResp)
	}

	// ② 定向 send：与广播消息同队列，供对比 poll 键形态。
	rec2 := httptest.NewRecorder()
	h.Send(rec2, msgAgentRequest("game-1", map[string]any{
		"messageId": uuidV7AtHandler(nowMs+1, "d1"), "msgType": "chat",
		"targetKind": "server", "targetServerId": "game-2", "payload": "hi",
	}))
	if rec2.Code != http.StatusOK {
		t.Fatalf("定向 send 应 200，实际 %d：%s", rec2.Code, rec2.Body.String())
	}

	// ③ game-2 poll：取到广播 + 定向两条；广播条带 broadcast:true，定向条不带该键（additive 语义）。
	rec3 := httptest.NewRecorder()
	h.Poll(rec3, msgAgentRequest("game-2", map[string]any{"waitSec": 0, "max": 10}))
	if rec3.Code != http.StatusOK {
		t.Fatalf("poll 应 200，实际 %d：%s", rec3.Code, rec3.Body.String())
	}
	var pollResp struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &pollResp); err != nil || len(pollResp.Messages) != 2 {
		t.Fatalf("poll 应取到 2 条，实际 %s err=%v", rec3.Body.String(), err)
	}
	for _, m := range pollResp.Messages {
		switch m["msgType"] {
		case "announce":
			if m["broadcast"] != true {
				t.Fatalf("广播下发应带 broadcast:true，实际 %v", m)
			}
			payload, ok := m["payload"].(map[string]any)
			if !ok || payload["text"] != "hello" {
				t.Fatalf("广播结构化 payload 应保持对象类型，实际 %v", m["payload"])
			}
		case "chat":
			if _, has := m["broadcast"]; has {
				t.Fatalf("定向下发不应带 broadcast 键，实际 %v", m)
			}
		default:
			t.Fatalf("非预期消息 %v", m)
		}
	}

	// ④ 空在线 zone 广播：200 status=failed（无在线目标）。
	rec4 := httptest.NewRecorder()
	h.Send(rec4, msgAgentRequest("game-1", map[string]any{
		"messageId": uuidV7AtHandler(nowMs+2, "b2"), "msgType": "announce",
		"targetKind": "broadcast", "targetZone": "no-such-zone", "payload": "x",
	}))
	var failResp map[string]any
	_ = json.Unmarshal(rec4.Body.Bytes(), &failResp)
	if rec4.Code != http.StatusOK || failResp["status"] != "failed" {
		t.Fatalf("空在线集合广播应 200 failed，实际 %d %v", rec4.Code, failResp)
	}
}
