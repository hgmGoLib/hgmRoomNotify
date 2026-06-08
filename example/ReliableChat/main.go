// ReliableChat: 用 hgmRoomNotify(ws 戳一下) + ajax(取真实数据) 实现可靠聊天消息送达
// 以及离线消息/历史/回放/断线补发. 完整对接逻辑见同目录其它文件, 自动测试见 chat_test.go.
//
// 运行: go run ./example/ReliableChat
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

func main() {
	const roomId = "chat:room1"

	// 1. 起服务端: chatStore(数据库) + ws 戳一下层 + /chat/after ajax 接口.
	store := &chatStore_t{}
	cs := newChatServer(store)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() { _ = http.Serve(ln, cs.Handler()) }()
	httpBase := "http://" + addr
	wsUrl := "ws://" + addr + "/ws"
	fmt.Printf("server listening on %s\n", addr)

	// 2. 房间里先存几条历史消息(模拟用户上线前别人已经聊过).
	store.Post(roomId, "alice", "历史消息1")
	store.Post(roomId, "bob", "历史消息2")

	// 3. 客户端上线进房 -> 进房会触发一次 onChange(空 LiveData)-> 自动 ajax 把历史拉回来(回放).
	cc := newChatClient(httpBase, wsUrl, "userC", roomId)
	defer cc.Close()
	waitUntil(func() bool { return cc.LastIndex() >= 2 }, 3*time.Second)
	fmt.Printf("上线后回放历史: 已应用 %d 条, ajax=%d\n", cc.LastIndex(), cc.ajaxCount.Load())

	// 4. 服务端逐条发新消息(每条之间留点间隔) -> 客户端走快路径(LiveData)直接应用, 0 额外 RTT.
	//    若不留间隔连发, onChange 会被合并成跳号, 客户端改走 ajax 补齐(见自动测试 BurstNoLoss).
	for i := 3; i <= 5; i++ {
		want := uint64(i)
		cs.Post(roomId, "alice", fmt.Sprintf("实时消息%d", i))
		waitUntil(func() bool { return cc.LastIndex() >= want }, 3*time.Second)
	}
	fmt.Printf("实时消息后: 已应用 %d 条, 快路径=%d, ajax=%d\n",
		cc.LastIndex(), cc.fastCount.Load(), cc.ajaxCount.Load())

	for _, m := range cc.Snapshot() {
		fmt.Printf("  [%d] %s: %s\n", m.MsgIndex, m.Sender, m.Text)
	}
	fmt.Println("demo done.")
}

// 轮询等待 cond 为真, 超时则 panic. 仅 demo 用.
func waitUntil(cond func() bool, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		log.Fatal("waitUntil timeout")
	}
}
