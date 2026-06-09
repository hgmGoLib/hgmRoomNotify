package hgmRoomNotify

import (
	"strconv"
	"sync"
	"time"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibSync"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibCloser"
	"sync/atomic"
	"net/http"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibTimer"
	"context"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibWebsocket2"
)
type ClientWsDialReq_t struct {
	Url             string // like wss://10.10.10.10:5845/ws
	CookieS         string // like wssSession=abc
	EnableTlsVerify bool   // 注意默认不验证tls证书(当前使用环境下 tls 证书太难搞了)
	Identity        string // in-band identity(类似 sessionId/token). 每次(重)连后发给服务端. 可空. 本模块不解析.
}

// 服务端拒绝事件, 传给 OnDenyFn. IsConn=true 表示连接级拒绝(identity 认证失败);
// IsConn=false 表示房间级拒绝(进入 RoomId 被拒).
type ClientDeny_t struct {
	IsConn bool
	RoomId string
	Reason string
}
/*
* 客户端连接状态分为:
    * 正常连接. (已确认正常连接上服务器)(注意,可能没有需求)
    * 无需求 (没有连接上,且没有需求)
    * 网络失败 (上次网络连接失败,并且有需求,并且当前没有连上)
    * 连接中 (上次没有连接失败,有需求,并且当前没有连上)
    * 已关闭 (调用者要求关闭本对象,后续一定不会再连接了)
 */
type Client struct {
	// 第一次连接或者重连时 连接的信息.
	// 用回调是为了让调用者可以动态修改 sessionId 参数.
	WsDialReqFn func() ClientWsDialReq_t
	// 服务端拒绝(连接或房间)的处理回调. 可选.
	// 不注册时: 服务端发来 deny -> 客户端判定为对接错误(应修复bug), 断开且不再重连, 后续 RoomEnter 报错.
	// 注册后默认行为: 连接级被拒 -> 连接保持/继续重连, 但进/离房间无效果, 新 RoomEnter 本地直接回 OnDenyFn(不找服务端), 重连时复位;
	//               房间级被拒 -> 该房间留在 intent 里(随重连自动重试), 只是当前不更新数据.
	OnDenyFn func(ev *ClientDeny_t)
	TimeoutCfg zlibSync.Var[TimeoutCfg_t]
	// 观测事件回调. nil 表示使用 ObsDefaultFn. 设置为空函数表示关闭观测.
	ObsFn func(ev *ObsEvent_t)
	// websocket message 最大读取字节数. 0表示使用默认值64KB, 负值 panic(_init 会把默认值写回本字段).
	// 必须 >= 服务端 WriteBufMaxBytes(单个 websocket message 最大可达该值), 否则会因消息过大断开.
	ReadMsgMaxBytes int

	thisConn atomic.Pointer[client_conn]
	lastStartConnectTime           time.Time

	noNeedIdleCloseTimer zlibTimer.TimerNoLock
	roomMap map[string]*client_room_t
	// 进入离开的请求列表
	roomIdReqSet map[string]struct{}

	roomNl zlibSync.NotifyList
	roomLock sync.Mutex

	initOnce sync.Once
	connSingleUnd zlibSync.SingleUnd

	// 用于中断 reconnect sleep. SetIsStopListen(true) 时关闭此 channel, 重新连接时重建.
	reconnectSleepCancelCh chan struct{}
	// 关闭整个client 功能, 目前仅用于自动测试 清理资源.请不要在其他情况下使用.
	closerForTest zlibCloser.Closer
	wsDialNum     atomic.Uint32
	sendEnterRoomMsgNum atomic.Uint32

	statusManager ClientStatusCtx_t
	isStopListen  zlibSync.Bool
	// 当前连接被服务端连接级拒绝(且注册了 OnDenyFn). 为 true 时 RoomEnter 本地直接回 OnDenyFn 不发服务端.
	// 每次(重)连开始时复位为 false, 收到连接级 deny 时置 true.
	connDenyLocal zlibSync.Bool
	// 服务端 deny 但没注册 OnDenyFn 的致命状态. 非空表示已断开且不再重连, 后续调用应报错.
	authDenyFatalMsg zlibSync.Var[string]

	// 跨连接的最后收到服务端有效消息的时间. 用于 GetUiStatusToUser.
	lastReadSuccTimeAll zlibSync.Time
	// 当前正在运行的 onChangeFn 回调数量.
	onChangeRunningCount atomic.Uint32
	// 非空表示客户端进入 needManual 状态的原因.
	needManualMsg zlibSync.Var[string]
}
func (c *Client)CloseForTest(){
	//c.logStatus(ClientStatusType_testClose,"")
	c.logClose(CloseReason_testClose,"")
	c.logStatus(ClientStatus_closeForTest)
	c.closerForTest.Close()
}
func (c *Client) IsConnectedSucc() bool{
	conn:=c.thisConn.Load()
	if conn==nil{
		return false
	}
	return conn.conn.closer.IsClose()==false
}
func (c *Client) GetWsDialNum() uint32{
	return c.wsDialNum.Load()
}
func (c *Client) GetSendEnterRoomMsgNum() uint32{
	return c.sendEnterRoomMsgNum.Load()
}
type client_conn struct {
	conn conn_frame_t
	timerClientLastReadToReconnect zlibTimer.TimerNoLock
	timerClientIdleToSendKeepAlive zlibTimer.TimerNoLock
	timerAuth         zlibTimer.TimerNoLock
	lastWriteSuccTime     zlibSync.Time
	lastReadSuccTime      zlibSync.Time
	lastKeepAliveSendTime zlibSync.Time
	c *Client

	// 连接级认证解析信号. 收到 Cmd_connAllow 或 Cmd_deny(conn) 或连接关闭时 close(authResolvedCh).
	// 主连接 goroutine 在发完 identity 后等待此信号, 通过后才发 roomEnter.
	authResolvedOnce sync.Once
	authResolvedCh   chan struct{}
	connApproved     bool // 仅在 authResolvedCh 关闭后由主 goroutine 读取.
	connDenied       bool

	// 大 LiveData 分块重组缓冲. roomValueMore 累积分片, 末条 roomValue 拼出完整 LiveData 后清空.
	// 仅在本连接的 readThread 单 goroutine 内读写, 无需加锁. 连接重建时随 client_conn 一起丢弃.
	roomValueReassembleBuf []byte
}
func (tc *client_conn) resolveAuth(){
	tc.authResolvedOnce.Do(func(){
		close(tc.authResolvedCh)
	})
}

// 在 enter 调用之后初始化.
// 可能会多次调用. 没有需求会退出, 停止监听会退出.
func (c *Client) _init_afterEnter(){
	c.connSingleUnd.Do(func() {
		// 这个里面阻塞等待连接关闭.
		c.initOnce.Do(func() {
			// ReadMsgMaxBytes: 0 把默认 64KB 写回字段本身, 负值是调用者 bug 直接 panic. 之后读取点直接读字段.
			if c.ReadMsgMaxBytes < 0 {
				panic("hgmRoomNotify: Client.ReadMsgMaxBytes must not be negative, got " + strconv.Itoa(c.ReadMsgMaxBytes))
			}
			if c.ReadMsgMaxBytes == 0 {
				c.ReadMsgMaxBytes = 64 * 1024 // 默认 64KB.
			}
			c.TimeoutCfg.LockCb(func(t *TimeoutCfg_t) {
				t.InitWithDefault()
			})
			c.closerForTest.AddOnClose(func() {
				conn:=c.thisConn.Load()
				if conn!=nil{
					conn.conn.closer.Close()
				}
				c.roomLock.Lock()
				c.roomNl.NotifyAll()
				c.roomLock.Unlock()
			})
		})
		c.roomLock.Lock()
		c.reconnectSleepCancelCh = make(chan struct{})
		c.roomLock.Unlock()
		for{
			isContinue:=c.tryConnOnceSync()
			if isContinue ==false{
				return
			}
		}
	})
}

func (c *Client) tryConnOnceSync() (isContinue bool){
	c.thisConn.Store(nil)
	if c.closerForTest.IsClose(){
		return false
	}
	if c.isStopListen.Get(){
		c.logStatus(ClientStatus_stopListen)
		return false
	}
	if c.authDenyFatalMsg.Get()!=""{
		// 服务端 deny 但没注册 OnDenyFn: 视为对接错误, 不再重连.
		return false
	}
	if c.HasNeed()==false{
		c.logStatus(ClientStatus_noConnectNoNeed)
		return false
	}
	// 控制开始连接的最小时间间隔。
	dur:=c.TimeoutCfg.Get().ClientReconnectMinDur - time.Now().Sub(c.lastStartConnectTime)
	if dur>0{
		c.logStatus(ClientStatus_waitReconnect)
		timer:=time.NewTimer(dur)
		// reconnectSleepCancelCh 由 SetIsStopListen 在锁内置 nil + close, 所以这里也必须在锁内取一次本地引用,
		// 否则与 SetIsStopListen 的写并发(虽然 select 落在 nil channel 上等价于不触发, 但裸读字段在 race detector 下报错).
		c.roomLock.Lock()
		cancelCh:=c.reconnectSleepCancelCh
		c.roomLock.Unlock()
		select{
		case <-timer.C:
		case <-c.closerForTest.GetCloseContext().Done():
			timer.Stop()
			return false
		case <-cancelCh:
			timer.Stop()
		}
		if c.HasNeed()==false{
			c.logStatus(ClientStatus_noConnectNoNeed)
			return false
		}
		if c.isStopListen.Get(){
			c.logStatus(ClientStatus_stopListen)
			return false
		}
	}
	c.logStatus(ClientStatus_dialing)
	c.lastStartConnectTime = time.Now()
	c.wsDialNum.Add(1)
	ctx2,cancelFn:=context.WithTimeout(c.closerForTest.GetCloseContext(),c.TimeoutCfg.Get().ClientWsDialTimeoutDur)
	dialReq:=c.WsDialReqFn()
	ctx3:=zlibWebsocket2.Client_ctx_t{
		ReqHeader: http.Header{},
		EnableTlsVerify: dialReq.EnableTlsVerify,
		Url: dialReq.Url,
		CloseContext: ctx2,
	}
	if dialReq.CookieS !=""{
		ctx3.ReqHeader.Add("Cookie",dialReq.CookieS)
	}
	ctx3.Call()
	cancelFn()
	if ctx3.ErrMsg!=""{
		c.logClose(CloseReason_dialFail,ctx3.ErrMsg)
		return true
	}
	c.logStatus(ClientStatus_connected)
	ctx3.Conn.MaxReadMsgSize = uint32(c.ReadMsgMaxBytes)
	thisConn:=&client_conn{
		conn: conn_frame_t{raw: &ctx3.Conn},
		c:    c,
		authResolvedCh: make(chan struct{}),
	}
	thisConn.conn.onLogClose = c.logClose
	thisConn.conn.closer.AddOnClose(func() {
		// 连接关闭也要解除主 goroutine 对认证信号的等待.
		thisConn.resolveAuth()
		c.roomLock.Lock()
		c.roomNl.NotifyAll()
		c.roomLock.Unlock()
	})
	c.thisConn.Store(thisConn)
	now:=time.Now()
	thisConn.lastReadSuccTime.Set(now)
	thisConn.lastWriteSuccTime.Set(now)
	thisConn.lastKeepAliveSendTime.Set(now)
	c.lastReadSuccTimeAll.Set(now)
	thisConn.timerClientIdleToSendKeepAlive.SetFn(func(){
		if thisConn.conn.closer.IsClose(){
			return
		}
		thisConn.lastKeepAliveSendTime.SetNow()
		thisConn.resetClientIdleToSendKeepAlive()
		thisConn.conn.writeMsg(Msg_t{
			Cmd: Cmd_ping,
		})
	})
	thisConn.resetClientIdleToSendKeepAlive()
	thisConn.conn.closer.AddOnClose(func(){
		thisConn.conn.raw.Close()
		thisConn.timerClientIdleToSendKeepAlive.Stop()
		thisConn.timerClientLastReadToReconnect.Stop()
	})
	thisConn.timerClientLastReadToReconnect.SetFn(func(){
		c.logClose(CloseReason_closeByReadTimeout,"")
		thisConn.conn.closer.Close2()
	})
	thisConn.timerClientLastReadToReconnect.After(c.TimeoutCfg.Get().ClientLastReadToReconnectDur)
	thisConn.conn.onReadFinishSucc = func() {
		now:=time.Now()
		thisConn.lastReadSuccTime.Set(now)
		c.lastReadSuccTimeAll.Set(now)
		thisConn.timerClientLastReadToReconnect.After(c.TimeoutCfg.Get().ClientLastReadToReconnectDur)
		thisConn.resetClientIdleToSendKeepAlive()
	}
	thisConn.conn.onWriteFinishSucc = func(){
		thisConn.lastWriteSuccTime.SetNow()
		thisConn.resetClientIdleToSendKeepAlive()
	}
	thisConn.conn.onReadMsg = func(msg Msg_t) {
		switch msg.Cmd {
		case Cmd_ping:
		case Cmd_setTimeCfg:
			cfg:=msg.TimeoutCfg
			if cfg!=nil{
				cfg.InitWithDefault()
				c.TimeoutCfg.Set(*cfg)
			}
		case Cmd_roomValue:
			// 前面有 roomValueMore 累积分片时, 本条即为分块序列的最后一片, 把累积分片拼到它前面;
			// 没有累积分片时(常见的不分块情形)直接用本条 LiveData, 零额外拷贝.
			liveData := msg.LiveData
			if thisConn.roomValueReassembleBuf != nil {
				liveData = append(thisConn.roomValueReassembleBuf, msg.LiveData...)
				thisConn.roomValueReassembleBuf = nil
			}
			c.onRoomValue(msg.RoomId, msg.RoomEpoch, msg.ChangeSeq, msg.CVersionId, liveData)
		case Cmd_roomValueMore:
			// 累积一段 LiveData 分片. 上限 = ReadMsgMaxBytes(整组分片本就在单个 websocket message 内, 不会超).
			if len(thisConn.roomValueReassembleBuf)+len(msg.LiveData) > c.ReadMsgMaxBytes{
				c.logClose(CloseReason_protocolNoMatch, "roomValue reassemble overflow")
				thisConn.conn.closer.Close2()
				return
			}
			thisConn.roomValueReassembleBuf = append(thisConn.roomValueReassembleBuf, msg.LiveData...)
		case Cmd_connAllow:
			// 连接被批准. 清掉连接级 deny 状态(重连重新认证通过), 唤醒主 goroutine 去发 roomEnter.
			c.connDenyLocal.Set(false)
			if msg.AuthEnabled && c.OnDenyFn==nil{
				// 服务端启用了认证但客户端没接 onDenyFn: 当场告警, 不必等线上第一次真 deny.
				_emitObs(c.ObsFn, func(ev *ObsEvent_t) {
					ev.Type = ObsEventType_clientNeedManual
					ev.CloseDetail = "server has auth enabled but OnDenyFn is nil"
				})
			}
			thisConn.connApproved = true
			thisConn.resolveAuth()
		case Cmd_deny:
			if c.OnDenyFn==nil{
				// 没注册 onDenyFn: 对接错误. 断开且不再重连, 后续调用报错.
				c.authDenyFatalMsg.Set("被服务端拒绝, 且客户端没有处理(未注册 OnDenyFn). reason="+msg.Reason)
				c.setNeedManual("被服务端拒绝, 且客户端没有处理(未注册 OnDenyFn). reason="+msg.Reason)
				thisConn.resolveAuth()
				thisConn.conn.closer.Close2()
				return
			}
			if msg.DenyScope==DenyScope_conn{
				// 连接级被拒: 连接保持/继续重连, 进/离房间无效果. 标记本地 deny, RoomEnter 本地直接回调.
				c.connDenyLocal.Set(true)
				thisConn.connDenied = true
				c.OnDenyFn(&ClientDeny_t{IsConn:true, Reason:msg.Reason})
				thisConn.resolveAuth()
			}else{
				// 房间级被拒: 房间留在 intent(随重连重试), 当前不更新数据. 只回调.
				c.OnDenyFn(&ClientDeny_t{IsConn:false, RoomId:msg.RoomId, Reason:msg.Reason})
			}
		case Cmd_closeConn:
			if msg.IsTemp{
				// 临时关闭: 关掉当前连接, 重连循环会自动重连(重连重新认证).
				c.logClose(CloseReason_serverCloseConnTemp, msg.Reason)
				thisConn.conn.closer.Close2()
			}else{
				// 永久关闭: 不再重连. 这是预期的正常终态(带 reason), 不是对接错误.
				c.SetIsStopListen(true)
				_emitObs(c.ObsFn, func(ev *ObsEvent_t) {
					ev.Type = ObsEventType_clientServerCloseConn
					ev.CloseDetail = msg.Reason
				})
			}
		}
	}
	go func(){
		thisConn.conn.readThread()
	}()
	if thisConn.conn.closer.IsClose(){
		return true
	}
	// 连上后先发 identity(永远发, 没传则为空串). 然后等连接级认证结果(Cmd_connAllow / Cmd_deny(conn) / 连接关闭).
	// 认证通过前不发 roomEnter, 保证服务端"先认证连接再认证房间"的顺序.
	thisConn.conn.writeMsg(Msg_t{Cmd: Cmd_identity, Identity: dialReq.Identity})
	select{
	case <-thisConn.authResolvedCh:
	case <-c.closerForTest.GetCloseChan():
		return true
	}
	if thisConn.conn.closer.IsClose(){
		return true
	}
	if thisConn.connDenied{
		// 连接级被拒(已注册 OnDenyFn): 不发进/离房间. 保持连接, 等连接关闭后重连(重连重新认证).
		select{
		case <-thisConn.conn.closer.GetCloseChan():
		case <-c.closerForTest.GetCloseChan():
		}
		return true
	}
	roomIdList:=[]string{}
	c.roomLock.Lock()
	for roomId:=range c.roomMap{
		roomIdList = append(roomIdList,roomId)
	}
	c.roomIdReqSet = map[string]struct{}{}
	c.roomLock.Unlock()
	for _,roomId:=range roomIdList{
		thisConn.conn.writeMsg(Msg_t{
			Cmd: Cmd_roomEnter,
			RoomId: roomId,
		})
	}
	c.sendEnterRoomMsgNum.Add(uint32(len(roomIdList)))
	if thisConn.conn.closer.IsClose(){
		return true
	}
	for{
		c.roomLock.Lock()
		if thisConn.conn.closer.IsClose(){
			c.roomLock.Unlock()
			return true
		}
		if len(c.roomIdReqSet)>0{
			sendMsgList:=[]Msg_t{}
			for roomId:=range c.roomIdReqSet{
				_,ok:=c.roomMap[roomId]
				if ok{
					c.sendEnterRoomMsgNum.Add(1)
					sendMsgList = append(sendMsgList,Msg_t{
						Cmd: Cmd_roomEnter,
						RoomId: roomId,
					})
				}else{
					sendMsgList = append(sendMsgList,Msg_t{
						Cmd: Cmd_roomLeave,
						RoomId: roomId,
					})
				}
			}
			c.roomIdReqSet = map[string]struct{}{}
			c.roomLock.Unlock()
			for _,msg:=range sendMsgList{
				thisConn.conn.writeMsg(msg)
			}
			continue
		}
		ticket:=c.roomNl.AddWaitTicket()
		c.roomLock.Unlock()
		c.roomNl.Wait(ticket)
	}
}

func (c *client_conn) resetClientIdleToSendKeepAlive(){
/*
客户端 idle 发送时间计算逻辑:
* 规则1: 距离上次keepAlive尝试发送的开始时间不能低于 ClientIdleToSendKeepAliveDur 时间.
* 规则2: 当前时间距离 上次读取成功时间 或者 上次写入成功时间 超过 ClientIdleToSendKeepAliveDur 应当尝试发送(注意:要优先满足规则1)两者应当发送时间的最小值.
 */
	now:=time.Now()
	dur:=c.c.TimeoutCfg.Get().ClientIdleToSendKeepAliveDur
	t1:=c.lastKeepAliveSendTime.Get().Add(dur)
	t2:=c.lastWriteSuccTime.Get().Add(dur)
	t3:=c.lastReadSuccTime.Get().Add(dur)
	minOfT2AndT3:=t2
	if minOfT2AndT3.After(t3){ // t2 和 t3 的最小值.
		minOfT2AndT3 = t3
	}
	if minOfT2AndT3.After(t1){ // 规则1
		t1 = minOfT2AndT3
	}
	dur = t1.Sub(now)
	c.timerClientIdleToSendKeepAlive.After(dur)
}

func (c *Client) SetWsDialUrl(url string){
	c.WsDialReqFn = func() (req ClientWsDialReq_t) {
		req.Url = url
		return req
	}
}
func (c *Client) SetIsStopListen(isStop bool){
	old:=c.isStopListen.SetAndReturnOld(isStop)
	if old==isStop{
		return
	}
	if isStop{
		thisConn:=c.thisConn.Load()
		if thisConn != nil{
			thisConn.conn.closer.Close2()
		}
		// 中断 reconnect sleep, 让连接循环尽快检测到 isStopListen.
		c.roomLock.Lock()
		ch:=c.reconnectSleepCancelCh
		c.reconnectSleepCancelCh = nil
		c.roomLock.Unlock()
		if ch!=nil{
			close(ch)
		}
	}else{
		c._init_afterEnter()
	}
}
func (c *Client) GetIsStopListen()(isStop bool){
	return c.isStopListen.Get()
}