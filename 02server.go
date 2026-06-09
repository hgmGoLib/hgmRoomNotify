package hgmRoomNotify

import (
	"time"
	"net/http"
	"sync"
	"sync/atomic"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibSync"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibTimer"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibChannel"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibWebsocket2"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibIdGen"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibCloser"
	"strconv"
)

// FireChange的输入参数.
type RoomEvent_t struct {
	RoomId string
	LiveData []byte // 可选. 调用者负责序列化. 超过 ServerManager.LiveDataMaxSize(默认1024) 则该次静默忽略(发 obs). 超过单 frame 时自动分块传输.
	CVersionId string // 可选. 自定义版本id. 最大100字节,超过panic. 用于调用者追踪实际数据变化.
}
type ServerManager struct{
	TimeoutCfg zlibSync.Var[TimeoutCfg_t]
	// 接受请求中间件. 用于从网络层(ws cookie/url query)读取网络层 sessionId(写入 ctx.SessionId),
	// 或直接终止请求(ctx.IsStop). 注意这是"网络层身份", 与客户端 in-band 发来的 identity 互相独立.
	OnAcceptFn func(ctx *ServerOnAccept_ctx_t)
	// 认证回调. 可选. nil 表示不认证(连接和进房全部放行, 和没有认证系统一样).
	// 同一个回调同时处理连接级(ctx.RoomId=="")和房间级(ctx.RoomId!="")认证, 由 ctx 区分.
	// 回调内不显式 ctx.Deny() 即视为允许. 回调是同步的, 可以阻塞(查库/调权限服务),
	// 阻塞期间该连接的 keepalive 与其它房间数据照常流动(回调跑在每条连接独立的 cmd goroutine 上, 不在读循环上).
	OnAllowFn func(ctx *ServerOnAllow_ctx_t)
	// 观测事件回调. nil 表示使用 ObsDefaultFn. 设置为空函数表示关闭观测.
	ObsFn func(ev *ObsEvent_t)
	// 单连接最大房间数. 0表示使用默认值1024. 超过上限会给客户端报错并断开连接.
	RoomEnterMaxPerConn int
	// 写入缓冲最大字节数(每条连接). 0表示使用默认值64KB, 最大可配置 2GB. 调用者发现缓冲满了服务端主动断开连接.
	// 注意: 调大后客户端的 ReadMsgMaxBytes 必须 >= 本值(单个 websocket message 最大可达本值), 否则客户端会因消息过大断开.
	// 本字段必须在首次调用 API(ServeHTTP/FireChange) 之前配置好, 之后不再读取也不可更改(无锁/无atomic).
	WriteBufMaxBytes int
	// 单次 LiveData 最大字节数. 0表示使用默认值1024, 最大可配置 512MB. 超过则该次 FireChange 的 LiveData 被静默丢弃(发 obs).
	// 必须 <= WriteBufMaxBytes 的 25%, 否则 _init 时 panic. 超过单 frame 上限时自动用 roomValueMore...roomValue 分块传输.
	// 本字段必须在首次调用 API(ServeHTTP/FireChange) 之前配置好, 之后不再读取也不可更改(无锁/无atomic).
	LiveDataMaxSize int

	// _init 时按上面两个配置解析出的实际生效值(含默认值与上限校验), 之后只读.
	writeBufMaxBytesResolved int
	liveDataMaxSizeResolved  int

	connCount atomic.Int64
	roomMapLock sync.Mutex
	roomMap map[string]*room_t
	emptyRoomCleanTimer zlibTimer.TimerNoLock
	sessionIdToConnListMapLock sync.Mutex
	sessionIdToConnListMap map[string]map[*server_conn_t]struct{}
	initOnce sync.Once
	closer zlibCloser.Closer
}
type server_conn_t struct{
	conn conn_frame_t
	lastReadTime time.Time
	timerServerLastReadToClose zlibTimer.TimerNoLock
	timerAuthTimeout zlibTimer.TimerNoLock
	timerAskStopListenTimeout zlibTimer.Timer
	writeBuf server_conn_write_buf_t
	remoteAddr string
	sessionId string
	s *ServerManager

	roomMap              map[string]*room_t
	isStopRoomEnter      bool
	hasSendAskClientStop bool

	// 认证相关. useAuthQueue=true(OnAllowFn!=nil)时 identity/roomEnter/roomLeave 走 cmdCh 串行处理,
	// 使 OnAllowFn 可阻塞而不卡读循环. connApproved/connDenied 仅由 cmdLoop goroutine 读写, 无需加锁.
	pub          ServerConn_t
	useAuthQueue bool
	connApproved bool
	connDenied   bool
	cmdCh        chan Msg_t
	registeredKeys map[string]struct{} // 本连接在 sessionIdToConnListMap 中注册过的 key(网络层 sessionId + in-band identity), 关闭时按此清理.

	obsCloseReason string
	obsCloseDetail string
	obsCloseOnce   sync.Once
}
// 设置关闭原因(仅第一次生效).
func (sconn *server_conn_t) setObsCloseReason(reason string, detail string) {
	sconn.obsCloseOnce.Do(func() {
		sconn.obsCloseReason = reason
		sconn.obsCloseDetail = detail
	})
}
type room_t struct {
	id string
	connSet map[*server_conn_t]struct{}
	RoomEpoch string // 房间纪元id. 房间创建时由 zlibIdGen.NewId() 生成. 每次房间被重建都会变化.
	ChangeSeq uint64 // 变化序号. 每次FireChange递增. 客户端用同RoomEpoch下的ChangeSeq大小判断是否是新变化.
	CVersionId string // 自定义版本id. 调用者通过RoomEvent_t.CVersionId设置.
	emptyTime time.Time // connSet为空时的时间. 零值表示非空.
}
// LiveData/写缓冲 配置上限.
const serverWriteBufDefaultBytes = 64 * 1024              // 写缓冲默认 64KB.
const serverWriteBufMaxBytesLimit = 2 * 1024 * 1024 * 1024 // 写缓冲最大 2GB.
const serverLiveDataDefaultSize = 1024                    // LiveData 默认上限 1024 字节.
const serverLiveDataMaxSizeLimit = 512 * 1024 * 1024      // LiveData 最大上限 512MB.

func (s *ServerManager) _init(){
	s.initOnce.Do(func(){
		bufMax := s.WriteBufMaxBytes
		if bufMax <= 0 {
			bufMax = serverWriteBufDefaultBytes
		}
		if bufMax > serverWriteBufMaxBytesLimit {
			panic("hgmRoomNotify: WriteBufMaxBytes too large, max 2GB, got " + strconv.Itoa(s.WriteBufMaxBytes))
		}
		liveMax := s.LiveDataMaxSize
		if liveMax <= 0 {
			liveMax = serverLiveDataDefaultSize
		}
		if liveMax > serverLiveDataMaxSizeLimit {
			panic("hgmRoomNotify: LiveDataMaxSize too large, max 512MB, got " + strconv.Itoa(s.LiveDataMaxSize))
		}
		// LiveData 上限不能超过写缓冲的 25%(否则单条大 LiveData 容易撑满缓冲导致断连).
		if liveMax > bufMax/4 {
			panic("hgmRoomNotify: LiveDataMaxSize(" + strconv.Itoa(liveMax) + ") must be <= 25% of WriteBufMaxBytes(" + strconv.Itoa(bufMax) + ")")
		}
		s.writeBufMaxBytesResolved = bufMax
		s.liveDataMaxSizeResolved = liveMax
		s.TimeoutCfg.LockCb(func(t *TimeoutCfg_t) {
			t.InitWithDefault()
		})
		s.roomMap = map[string]*room_t{}
		s.sessionIdToConnListMap = map[string]map[*server_conn_t]struct{}{}
		s.emptyRoomCleanTimer.SetFn(func(){
			s._cleanEmptyRooms()
		})
		s.emptyRoomCleanTimer.After(time.Hour)
		s.closer.AddOnClose(func(){
			s.emptyRoomCleanTimer.Stop()
		})
	})
}
// 关闭 ServerManager, 停止后台定时器. 只能调用一次.
func (s *ServerManager) Close(){
	s.closer.Close()
}
// 扫描并删除空房间超过1小时的记录.
func (s *ServerManager) _cleanEmptyRooms(){
	if s.closer.IsClose(){
		return
	}
	now:=time.Now()
	s.roomMapLock.Lock()
	for id,room:=range s.roomMap{
		if len(room.connSet)==0 && !room.emptyTime.IsZero() && now.Sub(room.emptyTime)>=time.Hour{
			delete(s.roomMap,id)
		}
	}
	s.roomMapLock.Unlock()
	s.emptyRoomCleanTimer.After(time.Hour)
}
func (s *ServerManager) ServeHTTP(w http.ResponseWriter, r *http.Request){
	s._init()
	ctx2:=ServerOnAccept_ctx_t{
		W: w,
		R: r,
	}
	if s.OnAcceptFn!=nil{
		s.OnAcceptFn(&ctx2)
		if ctx2.IsStop{
			return
		}
	}
	ctx3:=zlibWebsocket2.Server_ctx_t{
		W: w,
		R: r,
	}
	ctx3.Call()
	if ctx3.ErrMsg!=""{
		http.Error(w, ctx3.ErrMsg, 400)
		return
	}
	maxBufBytes:=s.writeBufMaxBytesResolved
	useAuthQueue := s.OnAllowFn != nil
	sconn :=&server_conn_t{
		roomMap:         map[string]*room_t{},
		remoteAddr:      r.RemoteAddr,
		sessionId:       ctx2.SessionId,
		s:               s,
		useAuthQueue:    useAuthQueue,
		connApproved:    !useAuthQueue, // 无认证时连接默认已批准.
	}
	sconn.pub = ServerConn_t{
		SessionIdNet: ctx2.SessionId,
		sconn:        sconn,
	}
	if useAuthQueue {
		maxRooms := s.RoomEnterMaxPerConn
		if maxRooms <= 0 {
			maxRooms = 1024
		}
		sconn.cmdCh = make(chan Msg_t, maxRooms+2)
	}
	// 写缓冲在每个发送批次前预留 websocket 帧头空间(GetFrameBufPrefixPreservedSize), 使 WriteFrame 原地写头零 copy.
	sconn.writeBuf = server_conn_write_buf_t{
		bipBuf: zlibChannel.NewFrame16BipBuf2(uint32(maxBufBytes), ctx3.Conn.GetFrameBufPrefixPreservedSize()),
		sconn:  sconn,
	}
	sconn.registerSessionKey(ctx2.SessionId)
	ctx3.Conn.MaxReadMsgSize = uint32(maxBufBytes)
	sconn.conn.raw = &ctx3.Conn
	// 未认证连接的超时关闭: 启用了认证(OnAllowFn!=nil)但连接还没认证通过(没收到 identity 或没批准).
	if useAuthQueue {
		sconn.timerAuthTimeout.SetFn(func(){
			_emitObs(s.ObsFn, func(ev *ObsEvent_t) {
				ev.Type = ObsEventType_serverAuthTimeout
				ev.RemoteAddr = sconn.remoteAddr
				ev.SessionId = sconn.sessionId
			})
			sconn.setObsCloseReason("authTimeout", "")
			sconn.conn.closer.Close2()
		})
		sconn.timerAuthTimeout.After(s.TimeoutCfg.Get().ServerAuthTimeoutDur)
	}
	sconn.conn.onLogClose = func(reason CloseReason_t, log string) {
		sconn.setObsCloseReason(string(reason), log)
		if reason == CloseReason_protocolNoMatch {
			_emitObs(s.ObsFn, func(ev *ObsEvent_t) {
				ev.Type = ObsEventType_serverProtocolError
				ev.RemoteAddr = sconn.remoteAddr
				ev.SessionId = sconn.sessionId
				ev.CloseDetail = log
			})
		}
	}
	sconn.conn.closer.AddOnClose(func() {
		sconn.writeBuf.markBroken()
		sconn.timerAuthTimeout.Stop()
		sconn.timerServerLastReadToClose.Stop()
		sconn.timerAskStopListenTimeout.Stop()
		sconn.conn.raw.Close()
		s.roomMapLock.Lock()
		sconn.isStopRoomEnter = true
		sconn.deleteAllFromAllRoom__NOLOCK()
		s.roomMapLock.Unlock()
		s.sessionIdToConnListMapLock.Lock()
		for key:=range sconn.registeredKeys{
			set1:=s.sessionIdToConnListMap[key]
			if set1!=nil{
				delete(set1,sconn)
				if len(set1) == 0{
					delete(s.sessionIdToConnListMap, key)
				}
			}
		}
		s.sessionIdToConnListMapLock.Unlock()
		s.connCount.Add(-1)
		_emitObs(s.ObsFn, func(ev *ObsEvent_t) {
			ev.Type = ObsEventType_serverConnClose
			ev.RemoteAddr = sconn.remoteAddr
			ev.SessionId = sconn.sessionId
			ev.CloseReason = sconn.obsCloseReason
			ev.CloseDetail = sconn.obsCloseDetail
		})
	})
	sconn.timerServerLastReadToClose.SetFn(func(){
		sconn.setObsCloseReason("readTimeout", "")
		sconn.conn.closer.Close2()
	})
	sconn.resetServerLastReadToClose(s)
	// 不可以直接使用 websocket 的ping/pong 功能。原因：无法获知 最后 收包时间，无法获知,web 端客户端 无法主动发 ping。
	sconn.conn.onReadFinishSucc = func() {
		sconn.resetServerLastReadToClose(s)
	}
	sconn.conn.onReadMsg = func(msg Msg_t) {
		switch msg.Cmd {
		case Cmd_ping:
			sconn.sendMsgNoBlock(msg)
			return
		case Cmd_identity:
			// 无认证时 identity 直接忽略; 有认证时进 cmd 队列由 cmdLoop 跑 OnAllowFn.
			if sconn.useAuthQueue {
				sconn.pushCmd(msg)
			}
			return
		case Cmd_roomEnter:
			if len(msg.RoomId) == 0 || len(msg.RoomId) > 1024 {
				sconn.setObsCloseReason("protocolError", "invalid roomId length="+strconv.Itoa(len(msg.RoomId)))
				sconn.conn.closer.Close2()
				return
			}
			if sconn.useAuthQueue {
				sconn.pushCmd(msg)
			} else {
				sconn.doRoomEnter(msg.RoomId)
			}
			return
		case Cmd_roomLeave:
			if sconn.useAuthQueue {
				sconn.pushCmd(msg)
			} else {
				sconn.doRoomLeave(msg.RoomId)
			}
			return
		default:
			_emitObs(s.ObsFn, func(ev *ObsEvent_t) {
				ev.Type = ObsEventType_serverUnknownCmd
				ev.RemoteAddr = sconn.remoteAddr
				ev.SessionId = sconn.sessionId
			})
			sconn.setObsCloseReason("unknownCmd", "")
			sconn.conn.closer.Close2()
			return
		}
	}
	s.connCount.Add(1)
	_emitObs(s.ObsFn, func(ev *ObsEvent_t) {
		ev.Type = ObsEventType_serverConnAccept
		ev.RemoteAddr = sconn.remoteAddr
		ev.SessionId = sconn.sessionId
	})
	// 注意: sendMsgNoBlock 可能启动 writeLoop goroutine, 写失败会触发 OnClose 中的 connCount.Add(-1).
	// 所以 connCount.Add(1) 必须在 sendMsgNoBlock 之前.
	cfg:=s.TimeoutCfg.Get()
	sconn.sendMsgNoBlock(Msg_t{
		Cmd: Cmd_setTimeCfg,
		TimeoutCfg: &cfg,
	})
	if useAuthQueue {
		// 启用认证: 不立即批准, 等客户端发 identity, cmdLoop 跑 OnAllowFn 决定. cmdLoop 也串行处理进/离房间.
		go sconn.cmdLoop()
	} else {
		// 无认证: 立即批准, 让客户端可以开始进房间. AuthEnabled=false 告诉客户端"本端不会拒人".
		sconn.sendMsgNoBlock(Msg_t{Cmd: Cmd_connAllow, AuthEnabled: false})
	}
	// 主goroutine阻塞在读线程. 写入线程由 writeBuf 按需异步启动.
	sconn.conn.readThread()
}

func (sconn *server_conn_t) sendMsgNoBlock(msg Msg_t){
	sconn.handlePushResult(sconn.writeBuf.pushMsg(msg), msg.RoomId)
}

// 处理 pushMsg/pushMsgsAtomic 的返回码: msgTooLarge/bufFull 发 obs 并主动关闭连接, broken 忽略.
func (sconn *server_conn_t) handlePushResult(result pushMsgResult_t, roomId string){
	switch result {
	case pushMsgResult_ok:
		return
	case pushMsgResult_msgTooLarge:
		_emitObs(sconn.s.ObsFn, func(ev *ObsEvent_t) {
			ev.Type = ObsEventType_serverMsgTooLarge
			ev.RemoteAddr = sconn.remoteAddr
			ev.SessionId = sconn.sessionId
			ev.RoomId = roomId
		})
		sconn.setObsCloseReason("msgTooLarge", "roomId="+roomId)
		sconn.conn.closer.Close2()
	case pushMsgResult_broken:
		// 连接已断开, 不需要再关闭.
	case pushMsgResult_bufFull:
		_emitObs(sconn.s.ObsFn, func(ev *ObsEvent_t) {
			ev.Type = ObsEventType_serverWriteBufFull
			ev.RemoteAddr = sconn.remoteAddr
			ev.SessionId = sconn.sessionId
			ev.RoomId = roomId
		})
		sconn.setObsCloseReason("writeBufFull", "roomId="+roomId)
		sconn.conn.closer.Close2()
	}
}

// 重置服务端读超时定时器. SetFn 在 ServeHTTP 中已初始化, 这里只重置时间.
func (conn *server_conn_t) resetServerLastReadToClose(s *ServerManager){
	conn.timerServerLastReadToClose.After(s.TimeoutCfg.Get().ServerLastReadToCloseDur)
}

// 有个房间发生了变化. 房间不存在时自动创建.
func (s *ServerManager) FireChange(ev RoomEvent_t){
	s._init()
	if ev.RoomId == "" {
		panic("hgmRoomNotify: FireChange RoomId must not be empty")
	}
	if len(ev.CVersionId) > 100 {
		panic("hgmRoomNotify: CVersionId too long, max 100 bytes, got " + strconv.Itoa(len(ev.CVersionId)))
	}
	var liveBuf []byte
	if len(ev.LiveData) > 0 && len(ev.LiveData) <= s.liveDataMaxSizeResolved {
		liveBuf = ev.LiveData
	} else if len(ev.LiveData) > s.liveDataMaxSizeResolved {
		_emitObs(s.ObsFn, func(oev *ObsEvent_t) {
			oev.Type = ObsEventType_serverLiveDataDropped
			oev.RoomId = ev.RoomId
			oev.LiveDataLen = len(ev.LiveData)
		})
	}
	s.roomMapLock.Lock()
	room:=s.roomMap[ev.RoomId]
	if room==nil{
		room = &room_t{
			id:        ev.RoomId,
			connSet:   map[*server_conn_t]struct{}{},
			RoomEpoch: zlibIdGen.NewId(),
			emptyTime: time.Now(),
		}
		s.roomMap[ev.RoomId] = room
	}
	room.ChangeSeq++
	room.CVersionId = ev.CVersionId
	changeSeq:=room.ChangeSeq
	cVersionId:=room.CVersionId
	roomEpoch:=room.RoomEpoch
	connList:=make([]*server_conn_t,0,len(room.connSet))
	for conn:=range room.connSet{
		connList = append(connList,conn)
	}
	s.roomMapLock.Unlock()
	for _,conn:=range connList{
		conn.sendRoomValue(roomEpoch, changeSeq, ev.RoomId, cVersionId, liveBuf)
	}
	_emitObs(s.ObsFn, func(oev *ObsEvent_t) {
		oev.Type = ObsEventType_serverFireChange
		oev.RoomId = ev.RoomId
		oev.NotifiedCount = len(connList)
	})
}

type ServerOnAccept_ctx_t struct{
	W                     http.ResponseWriter
	R                     *http.Request
	IsStop                bool // 调用者已经该请求,终止框架处理.
	SessionId string // 网络层 sessionId. 与客户端 in-band identity 互相独立.
}

// 查询服务端当前状态快照.
func (s *ServerManager) GetSnapshot() ServerSnapshot_t {
	s.roomMapLock.Lock()
	roomCount := len(s.roomMap)
	s.roomMapLock.Unlock()
	return ServerSnapshot_t{
		ConnCount: int(s.connCount.Load()),
		RoomCount: roomCount,
	}
}

func (sconn *server_conn_t) deleteAllFromAllRoom__NOLOCK(){
	now:=time.Now()
	for _,room:=range sconn.roomMap{
		delete(room.connSet,sconn)
		if len(room.connSet)==0{
			room.emptyTime = now
		}
	}
	sconn.roomMap = nil
}
