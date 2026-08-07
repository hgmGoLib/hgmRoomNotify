package main

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hgmGoLib/hgmRoomNotify"
)

type tryConnectReq_t struct {
	Server     demoServer_t
	HttpClient *http.Client // nil 表示不设置 Client.HttpClient, 走本库内置的 http.Client.
	// 仅在 HttpClient==nil 时有效. HttpClient 非 nil 时本字段被本库忽略(tls 由 HttpClient 的 Transport 决定).
	EnableTlsVerify bool
}

type tryConnectResp_t struct {
	IsConnectedSucc bool
	// 连上之后服务端 FireChange 一次, 客户端收到几次变更通知. 0 表示没收到.
	GotFireChangeNum int
	// 最后一次连接失败的原因(tls 握手失败会出现在这里). 连上时为空.
	LastDialFailMsg string
}

// 用给定的 tls 设置连一次 demo 服务端, 进一个房间, 连上就让服务端 FireChange 一次看能不能收到.
// 返回后客户端已经停掉, 不再重连.
func tryConnect(req tryConnectReq_t) (resp tryConnectResp_t) {
	const roomId = "demoRoom"
	const waitDur = time.Second * 3

	client := &hgmRoomNotify.Client{}
	client.HttpClient = req.HttpClient
	client.WsDialReqFn = func() hgmRoomNotify.ClientWsDialReq_t {
		return hgmRoomNotify.ClientWsDialReq_t{
			Url:             req.Server.Url,
			EnableTlsVerify: req.EnableTlsVerify,
		}
	}
	lastDialFailMsg := atomic.Pointer[string]{}
	client.ObsFn = func(ev *hgmRoomNotify.ObsEvent_t) {
		if ev.Type == hgmRoomNotify.ObsEventType_clientConnDialFail {
			// ObsEvent_t 用 sync.Pool 复用, 回调外字段值不保证, 要用的字段必须自己复制一份.
			msg := ev.CloseDetail
			lastDialFailMsg.Store(&msg)
		}
	}
	changeNum := atomic.Uint32{}
	leaveFn := client.RoomEnter(roomId, func(ev *hgmRoomNotify.RoomOnChange_t) {
		changeNum.Add(1)
	})
	defer func() {
		leaveFn()
		client.SetIsStopListen(true)
	}()

	deadline := time.Now().Add(waitDur)
	for time.Now().Before(deadline) && client.IsConnectedSucc() == false {
		time.Sleep(time.Millisecond * 20)
	}
	resp.IsConnectedSucc = client.IsConnectedSucc()
	if msg := lastDialFailMsg.Load(); msg != nil {
		resp.LastDialFailMsg = *msg
	}
	if resp.IsConnectedSucc == false {
		return resp
	}
	// 进房间会先收到一次"当前版本"的初始变更, 等它到了再 FireChange, 只数 FireChange 引发的那次.
	for time.Now().Before(deadline) && changeNum.Load() == 0 {
		time.Sleep(time.Millisecond * 20)
	}
	baseNum := changeNum.Load()
	req.Server.WsServer.FireChange(hgmRoomNotify.RoomEvent_t{RoomId: roomId, CVersionId: "v1"})
	for time.Now().Before(deadline) && changeNum.Load() == baseNum {
		time.Sleep(time.Millisecond * 20)
	}
	resp.GotFireChangeNum = int(changeNum.Load() - baseNum)
	return resp
}
