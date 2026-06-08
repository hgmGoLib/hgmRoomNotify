package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/hgmGoLib/hgmRoomNotify"
)

// 聊天客户端: 包一层 hgmRoomNotify.Client, 实现 "ws 戳一下 + ajax 取真实数据" 的对接逻辑.
// 真正的消息列表在 applied 里, lastIndex 是已连续应用到的最大 MsgIndex.
type chatClient_t struct {
	client      hgmRoomNotify.Client
	httpBaseUrl string // 如 http://127.0.0.1:1234, ajax 拉消息用.
	roomId      string
	leaveFn     func()

	lock      sync.Mutex
	applied   []ChatMsg_t // 已应用的消息(连续, 下标 i 对应 MsgIndex i+1).
	lastIndex uint64      // 已连续应用到的最大 MsgIndex.

	fastCount atomic.Uint64 // 走快路径(直接用 LiveData)应用的消息数. 仅用于演示/断言.
	ajaxCount atomic.Uint64 // 触发 ajax 补取的次数. 仅用于演示/断言.
}

// 新建并连接一个聊天客户端, 自动进入 roomId 房间. sessionId 通过 cookie 带给服务端(用于演示踢连接重连).
func newChatClient(httpBaseUrl string, wsUrl string, sessionId string, roomId string) *chatClient_t {
	cc := &chatClient_t{httpBaseUrl: httpBaseUrl, roomId: roomId}
	cc.client.WsDialReqFn = func() hgmRoomNotify.ClientWsDialReq_t {
		return hgmRoomNotify.ClientWsDialReq_t{
			Url:     wsUrl,
			CookieS: "chatSession=" + sessionId,
		}
	}
	cc.leaveFn = cc.client.RoomEnter(roomId, cc.onChange)
	return cc
}

// 房间变更回调. 这是整个对接方案的核心.
func (cc *chatClient_t) onChange(ev *hgmRoomNotify.RoomOnChange_t) {
	// 快路径: LiveData 带着整条消息, 且正好接在 lastIndex 后面 -> 直接应用, 0 额外 RTT.
	// 这就是 "直接推送每一条聊天消息" 的低延迟路径.
	if len(ev.LiveData) > 0 {
		var m ChatMsg_t
		if json.Unmarshal(ev.LiveData, &m) == nil && m.RoomId == cc.roomId {
			cc.lock.Lock()
			switch {
			case m.MsgIndex <= cc.lastIndex:
				// 已经有了(重复/旧消息), 忽略, 不必 ajax.
				cc.lock.Unlock()
				return
			case m.MsgIndex == cc.lastIndex+1:
				cc.applied = append(cc.applied, m)
				cc.lastIndex = m.MsgIndex
				cc.fastCount.Add(1)
				cc.lock.Unlock()
				return
			}
			// 跳号: 中间漏了消息(连发被合并 / 断过线), 落到下面 ajax 补齐.
			cc.lock.Unlock()
		}
	}
	// 慢路径: LiveData 没有(被丢弃 / 超 1024 / 重连 / 服务器重启)或跳号 -> ajax 拉 lastIndex 之后的全部.
	// 这就是 "可靠送达兜底" 和 "离线消息/历史/回放/断线补发" 的统一入口.
	cc.backfillByAjax(ev)
}

// ajax 把 lastIndex 之后的消息全部拉回来补齐.
func (cc *chatClient_t) backfillByAjax(ev *hgmRoomNotify.RoomOnChange_t) {
	cc.lock.Lock()
	after := cc.lastIndex
	cc.lock.Unlock()

	msgs, err := cc.fetchAfter(after)
	cc.ajaxCount.Add(1)
	if err != nil {
		// 回调里设置 ErrMsg -> 客户端进入 needManual 状态, UI 应提示用户手动刷新.
		ev.ErrMsg = "chat ajax backfill failed: " + err.Error()
		return
	}
	cc.lock.Lock()
	defer cc.lock.Unlock()
	for _, m := range msgs {
		// 严格按序号连续追加, 避免重复/乱序.
		if m.MsgIndex == cc.lastIndex+1 {
			cc.applied = append(cc.applied, m)
			cc.lastIndex = m.MsgIndex
		}
	}
}

// 真实的 http GET, 拉取 MsgIndex > after 的消息.
func (cc *chatClient_t) fetchAfter(after uint64) ([]ChatMsg_t, error) {
	u := cc.httpBaseUrl + "/chat/after?roomId=" + url.QueryEscape(cc.roomId) +
		"&after=" + strconv.FormatUint(after, 10)
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out []ChatMsg_t
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// 当前已应用的消息快照(给演示/测试断言用).
func (cc *chatClient_t) Snapshot() []ChatMsg_t {
	cc.lock.Lock()
	defer cc.lock.Unlock()
	out := make([]ChatMsg_t, len(cc.applied))
	copy(out, cc.applied)
	return out
}

// 已连续应用到的最大 MsgIndex.
func (cc *chatClient_t) LastIndex() uint64 {
	cc.lock.Lock()
	defer cc.lock.Unlock()
	return cc.lastIndex
}

func (cc *chatClient_t) Close() {
	cc.leaveFn()
	cc.client.CloseForTest()
}
