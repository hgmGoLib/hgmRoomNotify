package webchat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hgmGoLib/hgmRoomNotify"
)

// 聊天服务端: 把 ChatStore_t(内存库) 和 hgmRoomNotify.ServerManager(ws 戳一下层) 接到一起,
// 再加一个 /chat/post 给浏览器发消息. wsServer 用 atomic.Pointer 持有, 方便演示 "ws 进程重启".
type ChatServer_t struct {
	store    *ChatStore_t
	wsServer atomic.Pointer[hgmRoomNotify.ServerManager]
}

func NewChatServer(store *ChatStore_t) *ChatServer_t {
	cs := &ChatServer_t{store: store}
	cs.wsServer.Store(cs.newWsServer())
	return cs
}

// 新建一个 ws 层实例. 从 cookie 读 sessionId(用于 CloseConnBySessionId 踢连接演示重连).
func (cs *ChatServer_t) newWsServer() *hgmRoomNotify.ServerManager {
	sm := &hgmRoomNotify.ServerManager{
		OnAcceptFn: func(ctx *hgmRoomNotify.ServerOnAccept_ctx_t) {
			if c, err := ctx.R.Cookie("chatSession"); err == nil {
				ctx.SessionId = c.Value
			}
		},
	}
	// 把最小重连间隔调小, 方便演示快速重连. 生产环境用默认值即可.
	sm.TimeoutCfg.LockCb(func(t *hgmRoomNotify.TimeoutCfg_t) {
		t.ClientReconnectMinDur = 100 * time.Millisecond
	})
	return sm
}

// 发一条聊天消息: 先写库分配 MsgIndex, 再 FireChange 把整条消息塞进 LiveData(尽力而为的快路径).
// LiveData 超过 LiveDataMaxSize 会被库静默丢弃(只发通知不带 LiveData), 客户端那边自动走 ajax 兜底.
func (cs *ChatServer_t) Post(roomId string, sender string, text string) ChatMsg_t {
	m := cs.store.Post(roomId, sender, text)
	live, _ := json.Marshal(m)
	cs.wsServer.Load().FireChange(hgmRoomNotify.RoomEvent_t{
		RoomId:     roomId,
		CVersionId: strconv.FormatUint(m.MsgIndex, 10),
		LiveData:   live,
	})
	return m
}

// 路由. pageHtml 是要在 "/" 返回的页面(内嵌打包好的前端 JS). 同一套路由 main 和自动测试复用.
//   - /ws         : hgmRoomNotify 的通知通道(戳一下).
//   - /chat/after : ajax 取真实消息数据(GET, 返回 MsgIndex > after 的全部).
//   - /chat/post  : 浏览器发消息(POST).
//   - /           : 前端页面, 顺便给浏览器种一个 chatSession cookie(用于断线重连演示).
func (cs *ChatServer_t) Routes(pageHtml string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		cs.wsServer.Load().ServeHTTP(w, r)
	})
	mux.HandleFunc("/chat/after", cs.handleAfter)
	mux.HandleFunc("/chat/post", cs.handlePost)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if _, err := r.Cookie("chatSession"); err != nil {
			http.SetCookie(w, &http.Cookie{Name: "chatSession", Value: newSessionId(), Path: "/"})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageHtml))
	})
	return mux
}

// ajax 接口: 返回 roomId 房间内 MsgIndex > after 的全部消息(客户端 lastIndex 之后还没拿到的).
func (cs *ChatServer_t) handleAfter(w http.ResponseWriter, r *http.Request) {
	roomId := r.URL.Query().Get("roomId")
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	msgs := cs.store.After(roomId, after)
	if msgs == nil {
		msgs = []ChatMsg_t{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msgs)
}

// 浏览器发消息接口(POST JSON {RoomId,Sender,Text}). 写库 + FireChange, 返回写入后的完整消息.
func (cs *ChatServer_t) handlePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RoomId string
		Sender string
		Text   string
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.RoomId == "" || req.Sender == "" || req.Text == "" {
		http.Error(w, "roomId/sender/text required", http.StatusBadRequest)
		return
	}
	m := cs.Post(req.RoomId, req.Sender, req.Text)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}

// 生成一个随机 sessionId.
func newSessionId() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
