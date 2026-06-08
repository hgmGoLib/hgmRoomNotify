package hgmRoomNotify

import "sync"
/*
客户端日志 模块.
 */

type ClientStatus_t struct {
	Type            ClientStatusType_t // 当前状态
	HasNeed         bool               // 当前是否有需求.
	LastCloseReason CloseReason_t      // 最后关闭原因.(注意当前处于链接中的时候,这两项没有)
	LastCloseLog    string             // 最后关闭时的详细信息.(注意当前处于链接中的时候,这两项没有)
	IsStopListen bool // 当前是否停止监听.
}
func (c *Client) GetClientStatus() ClientStatus_t{
	thisLog:= ClientStatus_t{}
	c.statusManager.lock.Lock()
	thisLog.Type = c.statusManager.Type
	thisLog.LastCloseReason = c.statusManager.LastCloseReason
	thisLog.LastCloseLog = c.statusManager.LastCloseLog
	c.statusManager.lock.Unlock()
	thisLog.HasNeed = c.HasNeed()
	if thisLog.HasNeed==false && thisLog.Type==ClientStatus_connected{
		thisLog.Type = ClientStatus_connectedNoNeed
	}
	thisLog.IsStopListen = c.isStopListen.Get()
	if thisLog.IsStopListen{
		thisLog.Type = ClientStatus_stopListen
	}
	return thisLog
}
// 表示当前客户端的状态.
type ClientStatusType_t string
const ClientStatus_noConnectNoNeed ClientStatusType_t = "noConnectNoNeed" // 当前没有需求,并且没有链接(注意有需求了还能链接)
const ClientStatus_dialing ClientStatusType_t = "dialing"                 // 链接中
const ClientStatus_waitReconnect ClientStatusType_t = "waitReconnect"     // 等待重连 (在 ClientReconnectMinDur 时间内)
const ClientStatus_connected ClientStatusType_t = "connected"             // 客户端认为自己已经连上了,没有断开,并且当前有需求.
const ClientStatus_connectedNoNeed ClientStatusType_t = "connectedNoNeed" // 当前没有需求,链接上了 (在 ClientNoNeedIdleDur 时间内)
const ClientStatus_closeForTest ClientStatusType_t = "closeForTest"       // 测试关闭了链接.
const ClientStatus_stopListen ClientStatusType_t = "stopListen" // 停止监听

type CloseReason_t string
const CloseReason_dialFail CloseReason_t = "dialFail"
const CloseReason_readFail CloseReason_t = "readFail"
const CloseReason_writeFail CloseReason_t = "writeFail"
const CloseReason_protocolNoMatch CloseReason_t = "protocolNoMatch"
const CloseReason_closeByReadTimeout CloseReason_t = "closeByReadTimeout"
const CloseReason_testClose CloseReason_t = "testClose"
const CloseReason_clientNoNeed CloseReason_t = "clientNoNeed"
const CloseReason_serverCloseConnTemp CloseReason_t = "serverCloseConnTemp"

func (c *Client) logStatus(Status ClientStatusType_t){
	c.statusManager.lock.Lock()
	if Status==ClientStatus_dialing{
		c.statusManager.CanSetCloseReason = true
	}else if Status==ClientStatus_connected{
		c.statusManager.CanSetCloseReason = true
		c.statusManager.LastCloseReason = ""
		c.statusManager.LastCloseLog = ""
	}
	c.statusManager.Type = Status
	c.statusManager.lock.Unlock()
	switch Status {
	case ClientStatus_dialing:
		_emitObs(c.ObsFn, func(ev *ObsEvent_t) { ev.Type = ObsEventType_clientConnDialing })
	case ClientStatus_connected:
		_emitObs(c.ObsFn, func(ev *ObsEvent_t) { ev.Type = ObsEventType_clientConnConnected })
	case ClientStatus_waitReconnect:
		_emitObs(c.ObsFn, func(ev *ObsEvent_t) { ev.Type = ObsEventType_clientConnWaitReconnect })
	case ClientStatus_noConnectNoNeed:
		_emitObs(c.ObsFn, func(ev *ObsEvent_t) { ev.Type = ObsEventType_clientConnNoNeed })
	}
}

func (c *Client) logClose(LastCloseReason CloseReason_t,log string){
	// 这里不记录关闭,因为状态转移大部分情况下不在关闭这一步.
	c.statusManager.lock.Lock()
	// 这里需要仅保留第一次报错原因,因为 读包超时的时候,读线程必然会报错.写线程有可能报错.
	if c.statusManager.CanSetCloseReason==false{
		c.statusManager.lock.Unlock()
		return
	}
	c.statusManager.LastCloseReason = LastCloseReason
	c.statusManager.LastCloseLog = log
	c.statusManager.CanSetCloseReason = false
	c.statusManager.lock.Unlock()
	switch LastCloseReason {
	case CloseReason_dialFail:
		_emitObs(c.ObsFn, func(ev *ObsEvent_t) {
			ev.Type = ObsEventType_clientConnDialFail
			ev.CloseDetail = log
		})
	case CloseReason_testClose:
		// 测试关闭不发射事件
	default:
		_emitObs(c.ObsFn, func(ev *ObsEvent_t) {
			ev.Type = ObsEventType_clientConnClose
			ev.CloseReason = string(LastCloseReason)
			ev.CloseDetail = log
		})
	}
}

// 最后关闭原因日志.
type ClientStatusCtx_t struct {
	lock            sync.Mutex
	Type            ClientStatusType_t
	LastCloseReason CloseReason_t
	LastCloseLog      string
	CanSetCloseReason bool // 是否可以设置关闭原因(开始链接时,配置为 true,设置过第一个 关闭原因后配置为 false.)
}