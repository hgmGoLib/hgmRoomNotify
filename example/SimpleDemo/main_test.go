package main

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hgmGoLib/hgmRoomNotify"
)

// 正常路径自动测试: 进程内起服务端 + Go 客户端, 服务端 FireChange 三次, 客户端按序收到三次变更通知.
// 验证 SimpleDemo 演示的 "ws 戳一下" 最小对接闭环是通的.
func TestSimpleDemo_FireChangeReceived(t *testing.T) {
	const roomId = "order:12345"

	var wsServer hgmRoomNotify.ServerManager
	mux := http.NewServeMux()
	mux.Handle("/ws", &wsServer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	addr := ln.Addr().String()

	var client hgmRoomNotify.Client
	defer client.CloseForTest()
	client.SetWsDialUrl("ws://" + addr + "/ws")

	gotCh := make(chan string, 16)
	leaveFn := client.RoomEnter(roomId, func(ev *hgmRoomNotify.RoomOnChange_t) {
		gotCh <- ev.CVersionId
	})
	defer leaveFn()

	// 等客户端连上并进房间.
	waitFor(t, func() bool {
		return client.IsConnectedSucc() && client.GetRoomCount() > 0
	}, 3*time.Second, "客户端连接并进房间")

	// 进房会先收到一次初始变更(空版本), 丢弃它.
	select {
	case <-gotCh:
	case <-time.After(time.Second):
		t.Fatal("没有收到进房初始变更")
	}

	// 服务端逐次 FireChange, 客户端应按序收到对应版本.
	for _, want := range []string{"v1", "v2", "v3"} {
		wsServer.FireChange(hgmRoomNotify.RoomEvent_t{
			RoomId:     roomId,
			CVersionId: want,
			LiveData:   []byte(`{"version":"` + want + `"}`),
		})
		select {
		case got := <-gotCh:
			if got != want {
				t.Fatalf("版本不一致: want=%q got=%q", want, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("超时未收到变更 %q", want)
		}
	}
}

// 轮询等待 cond 为真, 超时 t.Fatal.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("waitFor timeout: %s", msg)
	}
}
