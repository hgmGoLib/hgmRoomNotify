// 一个最小可运行的 demo: 在同一个进程里同时跑 hgmRoomNotify 的服务端和 Go 客户端.
// 服务端起一个 http server 暴露 /ws, 客户端连上去加入一个房间,
// 然后服务端每隔一会儿 FireChange 一次, 客户端打印收到的变更.
//
// 运行: cd hgmRoomNotify/example && go run ./SimpleDemo
// 自动测试: cd hgmRoomNotify/example && go test ./SimpleDemo
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/hgmGoLib/hgmRoomNotify"
)

func main() {
	const roomId = "order:12345"

	// 1. 服务端: 注册 ServerManager 为 http.Handler.
	//    这里不配置 OnAllowFn, 表示不认证, 所有连接和房间全部放行.
	var wsServer hgmRoomNotify.ServerManager
	mux := http.NewServeMux()
	mux.Handle("/ws", &wsServer)

	// 监听一个系统随机分配的端口, 方便 demo 不和别的服务冲突.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() {
		_ = http.Serve(ln, mux)
	}()
	fmt.Printf("server listening on %s\n", addr)

	// 2. 客户端: 连上服务端, 加入房间, 收到变更就打印.
	var client hgmRoomNotify.Client
	client.SetWsDialUrl("ws://" + addr + "/ws")

	gotCh := make(chan string, 16)
	leaveFn := client.RoomEnter(roomId, func(ev *hgmRoomNotify.RoomOnChange_t) {
		liveData := ""
		if len(ev.LiveData) > 0 {
			liveData = string(ev.LiveData)
		}
		fmt.Printf("client onChange: room=%s changeSeq=%d cVersionId=%q liveData=%q\n",
			ev.RoomId, ev.ChangeSeq, ev.CVersionId, liveData)
		gotCh <- ev.CVersionId
	})
	defer leaveFn()

	// 等客户端连上并进房间.
	for i := 0; i < 100; i++ {
		if client.GetRoomCount() > 0 && client.IsConnectedSucc() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Printf("client uiStatus=%s connected=%v rooms=%d\n",
		client.GetUiStatusToUser(), client.IsConnectedSucc(), client.GetRoomCount())

	// 刚进房间会先收到一次"当前版本"的 onChange(此处版本还是空的), 先把它丢掉, 让下面的输出干净.
	select {
	case <-gotCh:
	case <-time.After(500 * time.Millisecond):
	}

	// 3. 服务端连续 FireChange 几次, 客户端应当逐次收到通知.
	versions := []string{"v1", "v2", "v3"}
	for _, v := range versions {
		wsServer.FireChange(hgmRoomNotify.RoomEvent_t{
			RoomId:     roomId,
			CVersionId: v,
			LiveData:   []byte(fmt.Sprintf(`{"version":%q}`, v)),
		})
		fmt.Printf("server FireChange %s\n", v)
		select {
		case got := <-gotCh:
			fmt.Printf("  -> client confirmed %s\n", got)
		case <-time.After(2 * time.Second):
			log.Fatalf("timeout waiting for client onChange of %s", v)
		}
		time.Sleep(200 * time.Millisecond)
	}

	fmt.Println("demo done.")
}
