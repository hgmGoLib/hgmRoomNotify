package main

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 起一个真实的进程内服务端(真 tcp 监听 + 真 http + 真 ws + 真 ajax). 返回 chatServer/store/地址 和清理函数.
func setupTestServer(t *testing.T) (cs *chatServer_t, store *chatStore_t, httpBase string, wsUrl string, cleanup func()) {
	t.Helper()
	store = &chatStore_t{}
	cs = newChatServer(store)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: cs.Handler()}
	go func() { _ = srv.Serve(ln) }()
	addr := ln.Addr().String()
	httpBase = "http://" + addr
	wsUrl = "ws://" + addr + "/ws"
	cleanup = func() {
		_ = srv.Close()
		_ = ln.Close()
	}
	return cs, store, httpBase, wsUrl, cleanup
}

// 轮询等待 cond, 超时 t.Fatal.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("waitFor timeout: %s", msg)
	}
}

// 断言客户端已应用的消息和 store 完全一致(数量/序号/发送者/内容).
func assertSameAsStore(t *testing.T, cc *chatClient_t, store *chatStore_t, roomId string) {
	t.Helper()
	want := store.After(roomId, 0)
	got := cc.Snapshot()
	if len(got) != len(want) {
		t.Fatalf("消息数量不一致: client=%d store=%d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条消息不一致:\n client=%+v\n store =%+v", i, got[i], want[i])
		}
	}
}

// 等客户端连上并把进房后的首次 onChange(回放历史)处理完(ajax 至少跑过一次).
func waitJoinReady(t *testing.T, cc *chatClient_t) {
	t.Helper()
	waitFor(t, func() bool {
		return cc.client.IsConnectedSucc() && cc.ajaxCount.Load() >= 1
	}, 3*time.Second, "客户端进房并完成首次回放")
}

// 需求1: 每条消息可靠送达. 逐条发, 每条都应走快路径(直接用 LiveData), 0 额外 RTT.
func TestReliableChat_FastPath(t *testing.T) {
	const roomId = "chat:fast"
	cs, store, httpBase, wsUrl, cleanup := setupTestServer(t)
	defer cleanup()

	cc := newChatClient(httpBase, wsUrl, "userFast", roomId)
	defer cc.Close()
	waitJoinReady(t, cc) // 空房间进房: ajaxCount 变 1, lastIndex 0.
	ajaxAfterJoin := cc.ajaxCount.Load()

	for i := 1; i <= 5; i++ {
		cs.Post(roomId, "alice", "msg")
		want := uint64(i)
		waitFor(t, func() bool { return cc.LastIndex() >= want }, 3*time.Second, "等待第 N 条到达")
	}

	assertSameAsStore(t, cc, store, roomId)
	if cc.fastCount.Load() != 5 {
		t.Fatalf("期望 5 条都走快路径, 实际 fastCount=%d", cc.fastCount.Load())
	}
	if cc.ajaxCount.Load() != ajaxAfterJoin {
		t.Fatalf("逐条发不该触发额外 ajax, ajaxCount 从 %d 变成 %d", ajaxAfterJoin, cc.ajaxCount.Load())
	}
}

// 需求1兜底: 连发被合并(onChange 合并)导致跳号时, 用 ajax 补齐, 一条都不丢.
func TestReliableChat_BurstNoLoss(t *testing.T) {
	const roomId = "chat:burst"
	cs, store, httpBase, wsUrl, cleanup := setupTestServer(t)
	defer cleanup()

	cc := newChatClient(httpBase, wsUrl, "userBurst", roomId)
	defer cc.Close()
	waitJoinReady(t, cc)

	const n = 50
	for i := 0; i < n; i++ {
		cs.Post(roomId, "bob", "burst")
	}
	waitFor(t, func() bool { return cc.LastIndex() >= n }, 5*time.Second, "突发消息全部到达")
	assertSameAsStore(t, cc, store, roomId)
}

// 需求1兜底: LiveData 超 1024 字节被库丢弃时, 客户端发现没有 LiveData -> 走 ajax 补取, 不丢消息.
func TestReliableChat_OversizeLiveData(t *testing.T) {
	const roomId = "chat:oversize"
	cs, store, httpBase, wsUrl, cleanup := setupTestServer(t)
	defer cleanup()

	cc := newChatClient(httpBase, wsUrl, "userBig", roomId)
	defer cc.Close()
	waitJoinReady(t, cc)

	cs.Post(roomId, "alice", "small") // 第1条走快路径.
	waitFor(t, func() bool { return cc.LastIndex() >= 1 }, 3*time.Second, "小消息到达")
	fastBefore := cc.fastCount.Load()

	bigText := strings.Repeat("x", 2000) // JSON 编码后 >1024, LiveData 会被丢弃.
	cs.Post(roomId, "alice", bigText)
	waitFor(t, func() bool { return cc.LastIndex() >= 2 }, 3*time.Second, "大消息经 ajax 到达")

	assertSameAsStore(t, cc, store, roomId)
	if cc.fastCount.Load() != fastBefore {
		t.Fatalf("超大 LiveData 那条不该走快路径, fastCount 从 %d 变成 %d", fastBefore, cc.fastCount.Load())
	}
	if got := cc.Snapshot()[1].Text; got != bigText {
		t.Fatalf("大消息内容不对, 长度=%d", len(got))
	}
}

// 需求2: 离线消息/历史/回放. 房间已有历史, 新客户端上线进房 -> 自动 ajax 把历史全部拉回来.
func TestReliableChat_HistoryReplayOnJoin(t *testing.T) {
	const roomId = "chat:history"
	_, store, httpBase, wsUrl, cleanup := setupTestServer(t)
	defer cleanup()

	// 客户端上线前, 房间里已经有 5 条历史(别人聊的). 只写库, 不 FireChange(此时没有客户端在线).
	for i := 0; i < 5; i++ {
		store.Post(roomId, "someone", "old")
	}

	cc := newChatClient(httpBase, wsUrl, "userLate", roomId)
	defer cc.Close()
	waitFor(t, func() bool { return cc.LastIndex() >= 5 }, 3*time.Second, "进房回放 5 条历史")
	assertSameAsStore(t, cc, store, roomId)
}

// 需求2: 断线补发 / 服务器重启. ws 层重启 + 期间产生离线消息 -> 客户端重连后通过 ajax 补齐.
func TestReliableChat_ReconnectBackfill(t *testing.T) {
	const roomId = "chat:reconnect"
	const sessionId = "userReconn"
	cs, store, httpBase, wsUrl, cleanup := setupTestServer(t)
	defer cleanup()

	cc := newChatClient(httpBase, wsUrl, sessionId, roomId)
	defer cc.Close()
	waitJoinReady(t, cc)

	// 在线时先收到 2 条(快路径).
	cs.Post(roomId, "alice", "online1")
	cs.Post(roomId, "alice", "online2")
	waitFor(t, func() bool { return cc.LastIndex() >= 2 }, 3*time.Second, "在线消息到达")
	dialBefore := cc.client.GetWsDialNum()

	// ws 层重启(换新 ServerManager + 踢掉旧连接). store 不动.
	cs.RestartWs(sessionId)
	// 重启期间产生 3 条 "离线消息"(只写库, 客户端此刻没连上).
	for i := 0; i < 3; i++ {
		store.Post(roomId, "bob", "offline")
	}

	// 客户端应自动重连(dial 次数增加), 重连后 re-enter 房间 -> epoch 变化触发 onChange -> ajax 补齐.
	waitFor(t, func() bool { return cc.client.GetWsDialNum() > dialBefore }, 5*time.Second, "客户端重连")
	waitFor(t, func() bool { return cc.LastIndex() >= 5 }, 5*time.Second, "断线期间的离线消息补齐")
	assertSameAsStore(t, cc, store, roomId)

	// 重连后实时消息恢复正常(快路径).
	cs.Post(roomId, "alice", "online3")
	waitFor(t, func() bool { return cc.LastIndex() >= 6 }, 3*time.Second, "重连后实时消息")
	assertSameAsStore(t, cc, store, roomId)
}
