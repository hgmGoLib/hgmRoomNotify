package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hgmGoLib/hgmRoomNotify"
)

// 聊天服务端: 把 chatStore_t(数据库) 和 hgmRoomNotify.ServerManager(ws 戳一下层) 接到一起.
// wsServer 用 atomic.Pointer 持有, 是为了能演示 "ws 进程重启"(换一个新的 ServerManager)而 store 不动.
type chatServer_t struct {
	store    *chatStore_t
	wsServer atomic.Pointer[hgmRoomNotify.ServerManager]
}

func newChatServer(store *chatStore_t) *chatServer_t {
	cs := &chatServer_t{store: store}
	cs.wsServer.Store(cs.newWsServer())
	return cs
}

// 新建一个 ws 层实例. 从 cookie 读 sessionId(用于 CloseConnBySessionId 踢连接演示重连).
func (cs *chatServer_t) newWsServer() *hgmRoomNotify.ServerManager {
	sm := &hgmRoomNotify.ServerManager{
		OnAcceptFn: func(ctx *hgmRoomNotify.ServerOnAccept_ctx_t) {
			if c, err := ctx.R.Cookie("chatSession"); err == nil {
				ctx.SessionId = c.Value
			}
		},
	}
	// 把最小重连间隔调小, 方便演示/测试快速重连. 生产环境用默认值(5 秒)即可.
	sm.TimeoutCfg.LockCb(func(t *hgmRoomNotify.TimeoutCfg_t) {
		t.ClientReconnectMinDur = 100 * time.Millisecond
	})
	return sm
}

// 注册两条路由: /ws 是 hgmRoomNotify 的通知通道; /chat/after 是 ajax 取真实消息数据的接口.
func (cs *chatServer_t) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		cs.wsServer.Load().ServeHTTP(w, r)
	})
	mux.HandleFunc("/chat/after", cs.handleAfter)
	return mux
}

// 发一条聊天消息: 先写库分配 MsgIndex, 再 FireChange 把整条消息塞进 LiveData(尽力而为).
// LiveData 超过 1024 字节会被库静默丢弃(只发通知不带 LiveData), 客户端那边会自动走 ajax 兜底.
func (cs *chatServer_t) Post(roomId string, sender string, text string) ChatMsg_t {
	m := cs.store.Post(roomId, sender, text)
	live, _ := json.Marshal(m)
	cs.wsServer.Load().FireChange(hgmRoomNotify.RoomEvent_t{
		RoomId:     roomId,
		CVersionId: strconv.FormatUint(m.MsgIndex, 10),
		LiveData:   live,
	})
	return m
}

// 模拟 ws 进程重启: 新建 ServerManager 顶上, 踢掉旧连接(isTemp=true 让客户端重连到新实例), 关旧实例.
// store 不受影响, 所以重启期间/之前写入的消息都还在, 客户端重连后通过 ajax 补齐.
func (cs *chatServer_t) RestartWs(dropSessionId string) {
	old := cs.wsServer.Load()
	cs.wsServer.Store(cs.newWsServer())
	old.CloseConnBySessionId(dropSessionId, true, "ws restart")
	old.Close()
}

// ajax 接口: 返回 roomId 房间内 MsgIndex > after 的全部消息(客户端 lastIndex 之后还没拿到的).
func (cs *chatServer_t) handleAfter(w http.ResponseWriter, r *http.Request) {
	roomId := r.URL.Query().Get("roomId")
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	msgs := cs.store.After(roomId, after)
	if msgs == nil {
		msgs = []ChatMsg_t{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}
