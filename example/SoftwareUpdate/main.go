// SoftwareUpdate: 用 hgmRoomNotify 做"软件自动更新"对接的最小可运行示例.
//
// 流程: 客户端进程启动 10 秒后, 先问 check api(带上自己当前版本)拿到"要不要更新/下载地址"; 已是最新就进房,
// 用 ws 实时监听后续新版本发布. 服务端发布新版本时把版本号放进 CVersionId 实时戳一下, 客户端无需轮询即可知道.
//
// 关键区分: check api 是【可靠数据源】(要不要更新/去哪下, 以它为准); CVersionId 是【加速字段】
// (省去轮询, 实时戳一下), 不保证送达, 最终一律以 check api 为准。详见 doc/accelFieldsNotReliable.md。
//
// 运行: cd hgmRoomNotify/example && go run ./SoftwareUpdate
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

func main() {
	// 1. 起更新服务端. 初始最新版本 1.0.0(= 客户端将运行的版本, 所以启动检查时不需要更新).
	s := newUpdateServer(releaseInfo_t{Version: "1.0.0", DownloadURL: "http://downloads.example.com/app-1.0.0.bin"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() { _ = http.Serve(ln, s.Handler()) }()
	httpBase := "http://" + addr
	wsUrl := "ws://" + addr + "/ws"
	fmt.Printf("update server on %s, 最新版本 1.0.0\n", addr)

	// 2. 起一个运行 1.0.0 的客户端. 演示用 1 秒启动延迟(生产规范是 10 秒), 周期保底 check 设 3 秒
	//    (生产可设几小时; 这里调小只为体现"即使 ws 不通也会周期回源"。本 demo ws 正常, 实时路径会先命中)。
	uc := newUpdateClient(httpBase, wsUrl, "1.0.0", 1*time.Second, 3*time.Second)
	uc.Start()
	defer uc.Close()

	// 3. 启动检查: 已是最新, 不需要更新 -> 客户端转入 ws 监听.
	waitUntil(func() bool { return uc.checkCount.Load() >= 1 }, 5*time.Second)
	time.Sleep(300 * time.Millisecond) // 等进房稳定.
	fmt.Printf("启动检查: 当前 1.0.0 已是最新, pending=%v, 转入 ws 监听\n", uc.Pending())

	// 4. 服务端发布 1.1.0 -> ws 把 CVersionId=1.1.0 实时戳给客户端 -> 客户端发现比自己新 -> 回源 check -> 拿到下载地址.
	s.Publish("1.1.0", "http://downloads.example.com/app-1.1.0.bin")
	waitUntil(func() bool { return uc.Pending() != nil }, 5*time.Second)
	p := uc.Pending()
	fmt.Printf("收到实时更新通知: 需更新到 %s, 下载地址 %s\n", p.Version, p.DownloadURL)
	fmt.Printf("(回源 check 次数=%d, CVersionId 加速跳过次数=%d)\n", uc.checkCount.Load(), uc.skipCount.Load())
	fmt.Println("demo done.")
}

// 轮询等待 cond 为真, 超时则退出. 仅 demo 用.
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
