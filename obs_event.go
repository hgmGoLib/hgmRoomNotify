package hgmRoomNotify

import (
	"fmt"
	"strconv"
	"sync"
)
/*
可观测性事件系统.
调用者通过 ObsFn 回调接收事件, 决定是输出日志/做 metrics/还是两者都做.
ObsEvent_t 使用 sync.Pool 复用, 回调内有效, 离开回调后字段值不保证.
*/

type ObsEventType_t = uint8

// 服务端事件 1-19
const ObsEventType_serverConnAccept ObsEventType_t = 1
const ObsEventType_serverConnClose ObsEventType_t = 2
const ObsEventType_serverFireChange ObsEventType_t = 3
const ObsEventType_serverLiveDataDropped ObsEventType_t = 4
const ObsEventType_serverWriteBufFull ObsEventType_t = 5
const ObsEventType_serverRoomOverLimit ObsEventType_t = 6
const ObsEventType_serverUnknownCmd ObsEventType_t = 7
const ObsEventType_serverAuthTimeout ObsEventType_t = 8
const ObsEventType_serverCloseConnTimeout ObsEventType_t = 9
const ObsEventType_serverProtocolError ObsEventType_t = 10
const ObsEventType_serverMsgTooLarge ObsEventType_t = 11

// 客户端事件 20-39
const ObsEventType_clientConnDialing ObsEventType_t = 20
const ObsEventType_clientConnConnected ObsEventType_t = 21
const ObsEventType_clientConnDialFail ObsEventType_t = 22
const ObsEventType_clientConnWaitReconnect ObsEventType_t = 23
const ObsEventType_clientConnNoNeed ObsEventType_t = 24
const ObsEventType_clientConnClose ObsEventType_t = 25
const ObsEventType_clientServerCloseConn ObsEventType_t = 26
const ObsEventType_clientNeedManual ObsEventType_t = 27

// 观测事件. 使用 sync.Pool 复用, 回调内有效, 离开回调后字段值不保证.
// 如果回调内需要异步处理, 必须自行复制需要的字段.
type ObsEvent_t struct {
	Type          ObsEventType_t
	RemoteAddr    string // serverConnAccept/serverConnClose/serverWriteBufFull 等
	SessionId     string // serverConnAccept/serverConnClose 等
	RoomId        string // serverFireChange/serverLiveDataDropped/serverWriteBufFull 等
	CloseReason   string // serverConnClose/clientConnClose 时有效
	CloseDetail   string // serverConnClose/clientConnDialFail/clientConnClose 时有效
	NotifiedCount int    // serverFireChange 时有效
	LiveDataLen   int    // serverLiveDataDropped 时有效
}

func (ev *ObsEvent_t) Reset() { *ev = ObsEvent_t{} }

var obsEventPool = sync.Pool{
	New: func() any { return &ObsEvent_t{} },
}

// package 级别的默认观测函数. 初始值为 ObsDefaultStdoutFn.
// 设置为 nil 表示全局关闭默认观测.
var ObsDefaultFn func(ev *ObsEvent_t) = ObsDefaultStdoutFn

// 默认 stdout 输出函数. 只输出异常/正确性相关事件.
func ObsDefaultStdoutFn(ev *ObsEvent_t) {
	switch ev.Type {
	case ObsEventType_serverWriteBufFull:
		fmt.Println("hgmRoomNotify: serverWriteBufFull remoteAddr=" + ev.RemoteAddr + " roomId=" + ev.RoomId)
	case ObsEventType_serverRoomOverLimit:
		fmt.Println("hgmRoomNotify: serverRoomOverLimit remoteAddr=" + ev.RemoteAddr)
	case ObsEventType_serverUnknownCmd:
		fmt.Println("hgmRoomNotify: serverUnknownCmd remoteAddr=" + ev.RemoteAddr)
	case ObsEventType_serverAuthTimeout:
		fmt.Println("hgmRoomNotify: serverAuthTimeout remoteAddr=" + ev.RemoteAddr)
	case ObsEventType_serverProtocolError:
		fmt.Println("hgmRoomNotify: serverProtocolError remoteAddr=" + ev.RemoteAddr + " detail=" + ev.CloseDetail)
	case ObsEventType_serverLiveDataDropped:
		fmt.Println("hgmRoomNotify: serverLiveDataDropped roomId=" + ev.RoomId + " dataLen=" + strconv.Itoa(ev.LiveDataLen))
	case ObsEventType_serverCloseConnTimeout:
		fmt.Println("hgmRoomNotify: serverCloseConnTimeout remoteAddr=" + ev.RemoteAddr)
	case ObsEventType_serverMsgTooLarge:
		fmt.Println("hgmRoomNotify: serverMsgTooLarge remoteAddr=" + ev.RemoteAddr + " roomId=" + ev.RoomId)
	case ObsEventType_clientConnDialFail:
		fmt.Println("hgmRoomNotify: clientConnDialFail detail=" + ev.CloseDetail)
	case ObsEventType_clientConnClose:
		if ev.CloseReason != string(CloseReason_clientNoNeed) {
			fmt.Println("hgmRoomNotify: clientConnClose reason=" + ev.CloseReason + " detail=" + ev.CloseDetail)
		}
	}
}

// 转换为人类可读字符串.
func ObsEventType_toString(t ObsEventType_t) string {
	switch t {
	case ObsEventType_serverConnAccept: return "serverConnAccept"
	case ObsEventType_serverConnClose: return "serverConnClose"
	case ObsEventType_serverFireChange: return "serverFireChange"
	case ObsEventType_serverLiveDataDropped: return "serverLiveDataDropped"
	case ObsEventType_serverWriteBufFull: return "serverWriteBufFull"
	case ObsEventType_serverRoomOverLimit: return "serverRoomOverLimit"
	case ObsEventType_serverUnknownCmd: return "serverUnknownCmd"
	case ObsEventType_serverAuthTimeout: return "serverAuthTimeout"
	case ObsEventType_serverCloseConnTimeout: return "serverCloseConnTimeout"
	case ObsEventType_serverProtocolError: return "serverProtocolError"
	case ObsEventType_serverMsgTooLarge: return "serverMsgTooLarge"
	case ObsEventType_clientConnDialing: return "clientConnDialing"
	case ObsEventType_clientConnConnected: return "clientConnConnected"
	case ObsEventType_clientConnDialFail: return "clientConnDialFail"
	case ObsEventType_clientConnWaitReconnect: return "clientConnWaitReconnect"
	case ObsEventType_clientConnNoNeed: return "clientConnNoNeed"
	case ObsEventType_clientConnClose: return "clientConnClose"
	case ObsEventType_clientServerCloseConn: return "clientServerCloseConn"
	case ObsEventType_clientNeedManual: return "clientNeedManual"
	default: return "unknown(" + strconv.Itoa(int(t)) + ")"
	}
}

// 发射观测事件. obsFn 为 nil 时使用 ObsDefaultFn. 都为 nil 则不发射.
func _emitObs(obsFn func(ev *ObsEvent_t), setupFn func(ev *ObsEvent_t)) {
	fn := obsFn
	if fn == nil {
		fn = ObsDefaultFn
	}
	if fn == nil {
		return
	}
	ev := obsEventPool.Get().(*ObsEvent_t)
	defer func() {
		ev.Reset()
		obsEventPool.Put(ev)
	}()
	setupFn(ev)
	fn(ev)
}

// 服务端当前状态快照.
type ServerSnapshot_t struct {
	ConnCount int // 当前活跃连接数
	RoomCount int // 当前房间数
}
