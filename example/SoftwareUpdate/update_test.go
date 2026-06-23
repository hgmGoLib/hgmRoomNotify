package main

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// 起一个真实的进程内更新服务端(真 tcp + 真 http + 真 ws). 返回服务端/地址和清理函数.
func setupUpdateServer(t *testing.T, initial releaseInfo_t) (s *updateServer_t, httpBase string, wsUrl string, cleanup func()) {
	t.Helper()
	s = newUpdateServer(initial)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler()}
	go func() { _ = srv.Serve(ln) }()
	addr := ln.Addr().String()
	httpBase = "http://" + addr
	wsUrl = "ws://" + addr + "/ws"
	cleanup = func() {
		_ = srv.Close()
		_ = ln.Close()
	}
	return s, httpBase, wsUrl, cleanup
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

// 等客户端连上并进房(开始 ws 监听).
func waitListening(t *testing.T, uc *updateClient_t) {
	t.Helper()
	waitFor(t, func() bool {
		return uc.client.IsConnectedSucc() && uc.client.GetRoomCount() >= 1
	}, 5*time.Second, "客户端连上并进房监听")
}

// 启动时已是最新: check 一次说不用更新, 然后转入 ws 监听(pending 始终为 nil).
func TestStartupNoUpdate_ThenListen(t *testing.T) {
	s, httpBase, wsUrl, cleanup := setupUpdateServer(t, releaseInfo_t{Version: "1.0.0", DownloadURL: "http://dl/app-1.0.0.bin"})
	defer cleanup()
	_ = s

	uc := newUpdateClient(httpBase, wsUrl, "1.0.0", 20*time.Millisecond, time.Hour)
	uc.Start()
	defer uc.Close()

	waitFor(t, func() bool { return uc.checkCount.Load() >= 1 }, 5*time.Second, "启动后做了一次 check")
	waitListening(t, uc)
	if p := uc.Pending(); p != nil {
		t.Fatalf("已是最新不该有待安装版本, 却得到 %+v", p)
	}
}

// 启动时就需要更新: check 直接告知要更新到 2.0.0 + 下载地址, 不进房监听.
func TestStartupNeedUpdate(t *testing.T) {
	s, httpBase, wsUrl, cleanup := setupUpdateServer(t, releaseInfo_t{Version: "2.0.0", DownloadURL: "http://dl/app-2.0.0.bin"})
	defer cleanup()
	_ = s

	uc := newUpdateClient(httpBase, wsUrl, "1.0.0", 20*time.Millisecond, time.Hour)
	uc.Start()
	defer uc.Close()

	waitFor(t, func() bool { return uc.Pending() != nil }, 5*time.Second, "启动检查发现需要更新")
	p := uc.Pending()
	if p.Version != "2.0.0" || p.DownloadURL != "http://dl/app-2.0.0.bin" {
		t.Fatalf("待安装版本不对: %+v", p)
	}
	if got := uc.client.GetRoomCount(); got != 0 {
		t.Fatalf("启动就要更新时不该进房监听, GetRoomCount=%d", got)
	}
	if got := uc.checkCount.Load(); got != 1 {
		t.Fatalf("启动应只 check 一次, checkCount=%d", got)
	}
}

// 实时下发更新: 客户端已是最新并在监听, 服务端发布 1.1.0 -> ws 戳 CVersionId=1.1.0 -> 客户端回源拿到更新.
func TestPushUpdateViaWs(t *testing.T) {
	s, httpBase, wsUrl, cleanup := setupUpdateServer(t, releaseInfo_t{Version: "1.0.0", DownloadURL: "http://dl/app-1.0.0.bin"})
	defer cleanup()

	uc := newUpdateClient(httpBase, wsUrl, "1.0.0", 20*time.Millisecond, time.Hour)
	uc.Start()
	defer uc.Close()
	waitListening(t, uc)

	s.Publish("1.1.0", "http://dl/app-1.1.0.bin")
	waitFor(t, func() bool { return uc.Pending() != nil }, 5*time.Second, "ws 实时下发新版本后回源拿到更新")
	p := uc.Pending()
	if p.Version != "1.1.0" || p.DownloadURL != "http://dl/app-1.1.0.bin" {
		t.Fatalf("待安装版本不对: %+v", p)
	}
}

// 加速命中: 重复戳/重连回放带来的 CVersionId<=本地, 客户端直接跳过回源(不打 check api).
func TestDuplicatePokeSkipped(t *testing.T) {
	s, httpBase, wsUrl, cleanup := setupUpdateServer(t, releaseInfo_t{Version: "1.0.0", DownloadURL: "http://dl/app-1.0.0.bin"})
	defer cleanup()

	uc := newUpdateClient(httpBase, wsUrl, "1.0.0", 20*time.Millisecond, time.Hour)
	uc.Start()
	defer uc.Close()
	waitListening(t, uc)
	// 进房的首次 onChange(空 CVersionId)会回源一次 -> 等 checkCount 稳定到 2(启动 1 + 进房 1).
	waitFor(t, func() bool { return uc.checkCount.Load() >= 2 }, 5*time.Second, "进房首次回源完成")
	checkBefore := uc.checkCount.Load()
	skipBefore := uc.skipCount.Load()

	// 再发布一次相同版本(CVersionId=1.0.0, 不比客户端新)-> 应被跳过, 不回源.
	s.Publish("1.0.0", "http://dl/app-1.0.0.bin")
	waitFor(t, func() bool { return uc.skipCount.Load() > skipBefore }, 5*time.Second, "CVersionId<=本地被跳过")
	if got := uc.checkCount.Load(); got != checkBefore {
		t.Fatalf("CVersionId 命中应跳过回源, checkCount 从 %d 变成 %d", checkBefore, got)
	}
	if p := uc.Pending(); p != nil {
		t.Fatalf("相同版本不该产生待安装版本, 却得到 %+v", p)
	}
}

// 安全性: CVersionId 缺失(裸戳)时客户端必须回源, 不能因为"比不出来"就当成不用更新而跳过.
func TestEmptyCVersionIdForcesRecheck(t *testing.T) {
	s, httpBase, wsUrl, cleanup := setupUpdateServer(t, releaseInfo_t{Version: "1.0.0", DownloadURL: "http://dl/app-1.0.0.bin"})
	defer cleanup()

	uc := newUpdateClient(httpBase, wsUrl, "1.0.0", 20*time.Millisecond, time.Hour)
	uc.Start()
	defer uc.Close()
	waitListening(t, uc)
	waitFor(t, func() bool { return uc.checkCount.Load() >= 2 }, 5*time.Second, "进房首次回源完成")
	checkBefore := uc.checkCount.Load()
	skipBefore := uc.skipCount.Load()

	// 裸戳(FireChange 不带 CVersionId)-> 客户端必须回源, 不能跳过.
	s.NotifyBare()
	waitFor(t, func() bool { return uc.checkCount.Load() > checkBefore }, 5*time.Second, "空 CVersionId 强制回源")
	if got := uc.skipCount.Load(); got != skipBefore {
		t.Fatalf("空 CVersionId 不该走加速跳过, skipCount 从 %d 变成 %d", skipBefore, got)
	}
}

// 核心保底: ws 全程连不上(被封)时, 客户端仍靠周期 check 拿到更新. 证明 ws 只增加实时性、不影响正确性.
func TestWsBlockedStillUpdatesViaPoll(t *testing.T) {
	s, httpBase, _, cleanup := setupUpdateServer(t, releaseInfo_t{Version: "1.0.0", DownloadURL: "http://dl/app-1.0.0.bin"})
	defer cleanup()

	// wsUrl 指向一个连不上的地址(模拟 ws 被封). check api 仍指向真实服务端.
	deadWsUrl := "ws://127.0.0.1:1/ws"
	uc := newUpdateClient(httpBase, deadWsUrl, "1.0.0", 20*time.Millisecond, 50*time.Millisecond)
	uc.Start()
	defer uc.Close()

	// 启动 check 跑过(ws 连不上不影响它).
	waitFor(t, func() bool { return uc.checkCount.Load() >= 1 }, 5*time.Second, "启动 check 已跑(不依赖 ws)")

	// 服务端发布新版本. ws 没通, 不会有任何实时戳; 全靠周期保底 check 拿到.
	s.Publish("1.1.0", "http://dl/app-1.1.0.bin")
	waitFor(t, func() bool { return uc.Pending() != nil }, 5*time.Second, "ws 被封时靠周期 check 拿到更新")

	if uc.client.IsConnectedSucc() {
		t.Fatal("本用例前提是 ws 连不上, 但它却连上了")
	}
	if got := uc.skipCount.Load(); got != 0 {
		t.Fatalf("ws 没通不该有任何 CVersionId 加速命中, skipCount=%d", got)
	}
	p := uc.Pending()
	if p.Version != "1.1.0" {
		t.Fatalf("待安装版本不对: %+v", p)
	}
}
