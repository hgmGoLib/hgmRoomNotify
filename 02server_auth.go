package hgmRoomNotify

import (
	"strconv"
	"time"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibIdGen"
)

// 暴露给 OnAllowFn 的连接对象. 同一连接的多次回调拿到的是同一个对象(指针),
// 因此 Userdata 可以在连接级回调里设置, 在后续房间级回调里读取(跨调用传递数据).
type ServerConn_t struct {
	SessionIdNet   string // 网络层 sessionId(来自 OnAcceptFn, 如 ws cookie/url query). 可能为空.
	IdentityInBand string // 客户端 in-band 发来的 identity(Cmd_identity). 可能为空.
	Userdata       any    // 调用者可读写. 用于跨回调传递数据(如连接级解析出的角色, 房间级复用).
	sconn          *server_conn_t
}

// 要求客户端关闭当前连接. isTemp=true 客户端应重连(重连会重新认证); isTemp=false 客户端不再重连.
// reason 为原因文本. 用于禁用某 sessionId / 把客户端从某房间踢出(踢出靠重连时重新过 OnAllowFn).
func (c *ServerConn_t) CloseConn(isTemp bool, reason string) {
	c.sconn.closeConn(isTemp, reason)
}

// OnAllowFn 的回调参数. RoomId=="" 表示连接级认证(identity); RoomId!="" 表示房间级认证(进入该房间).
// 回调内不调用 Deny 即视为允许.
type ServerOnAllow_ctx_t struct {
	Conn       *ServerConn_t
	RoomId     string
	denied     bool
	denyReason string
}

// 拒绝本次(连接或房间). reason 为原因文本, 会下发给客户端.
func (ctx *ServerOnAllow_ctx_t) Deny(reason string) {
	ctx.denied = true
	ctx.denyReason = reason
}

// 把本连接注册到 sessionIdToConnListMap 的 key 下(网络层 sessionId 或 in-band identity).
// CloseConnBySessionId 通过此索引找到连接. key 为空忽略. 关闭时按 registeredKeys 清理.
func (sconn *server_conn_t) registerSessionKey(key string) {
	if key == "" {
		return
	}
	s := sconn.s
	s.sessionIdToConnListMapLock.Lock()
	defer s.sessionIdToConnListMapLock.Unlock()
	if sconn.registeredKeys == nil {
		sconn.registeredKeys = map[string]struct{}{}
	}
	if _, ok := sconn.registeredKeys[key]; ok {
		return
	}
	sconn.registeredKeys[key] = struct{}{}
	set := s.sessionIdToConnListMap[key]
	if set == nil {
		set = map[*server_conn_t]struct{}{}
		s.sessionIdToConnListMap[key] = set
	}
	set[sconn] = struct{}{}
}

// 把 identity/roomEnter/roomLeave 投递到 cmd 队列(由 cmdLoop 串行处理). 队列满表示客户端发太快(可能恶意), 关连接.
func (sconn *server_conn_t) pushCmd(msg Msg_t) {
	if sconn.conn.closer.IsClose() {
		return
	}
	select {
	case sconn.cmdCh <- msg:
	default:
		sconn.setObsCloseReason("cmdQueueFull", "")
		sconn.conn.closer.Close2()
	}
}

// 每条认证连接独立的命令处理 goroutine. 串行跑 OnAllowFn(可阻塞)而不卡读循环.
// connApproved/connDenied 仅本 goroutine 读写, 无需加锁.
func (sconn *server_conn_t) cmdLoop() {
	closeCh := sconn.conn.closer.GetCloseChan()
	for {
		select {
		case <-closeCh:
			return
		case msg := <-sconn.cmdCh:
			switch msg.Cmd {
			case Cmd_identity:
				sconn.handleIdentityAuth(msg.Identity)
			case Cmd_roomEnter:
				sconn.handleRoomEnterAuth(msg.RoomId)
			case Cmd_roomLeave:
				sconn.doRoomLeave(msg.RoomId)
			}
		}
	}
}

// 连接级认证: 收到 identity, 跑 OnAllowFn(连接级). 通过->发 Cmd_connAllow; 拒绝->发 Cmd_deny(conn).
func (sconn *server_conn_t) handleIdentityAuth(identity string) {
	if sconn.connDenied || sconn.connApproved {
		// 已决定过(重复 identity 或已批准). 忽略.
		return
	}
	sconn.pub.IdentityInBand = identity
	sconn.registerSessionKey(identity)
	ctx := &ServerOnAllow_ctx_t{Conn: &sconn.pub, RoomId: ""}
	sconn.s.OnAllowFn(ctx)
	sconn.timerAuthTimeout.Stop()
	if ctx.denied {
		sconn.connDenied = true
		sconn.sendMsgNoBlock(Msg_t{Cmd: Cmd_deny, DenyScope: DenyScope_conn, Reason: ctx.denyReason})
		return
	}
	sconn.connApproved = true
	sconn.sendMsgNoBlock(Msg_t{Cmd: Cmd_connAllow, AuthEnabled: true})
}

// 房间级认证: 跑 OnAllowFn(房间级). 通过->进房间(发 roomValue); 拒绝->发 Cmd_deny(room).
func (sconn *server_conn_t) handleRoomEnterAuth(roomId string) {
	if sconn.connDenied {
		// 连接级已拒绝, 忽略所有进房请求.
		return
	}
	if !sconn.connApproved {
		// 还没认证通过(非常规客户端先发了 roomEnter). 忽略, 等认证或超时.
		return
	}
	ctx := &ServerOnAllow_ctx_t{Conn: &sconn.pub, RoomId: roomId}
	sconn.s.OnAllowFn(ctx)
	if ctx.denied {
		sconn.sendMsgNoBlock(Msg_t{Cmd: Cmd_deny, DenyScope: DenyScope_room, RoomId: roomId, Reason: ctx.denyReason})
		return
	}
	sconn.doRoomEnter(roomId)
}

// 实际把连接加入房间并回发当前房间值(Cmd_roomValue). 无认证路径直接调用, 认证路径在 OnAllowFn 通过后调用.
func (sconn *server_conn_t) doRoomEnter(roomId string) {
	s := sconn.s
	s.roomMapLock.Lock()
	if sconn.isStopRoomEnter {
		s.roomMapLock.Unlock()
		return
	}
	maxRooms := s.RoomEnterMaxPerConn
	if maxRooms <= 0 {
		maxRooms = 1024
	}
	if len(sconn.roomMap) >= maxRooms {
		s.roomMapLock.Unlock()
		_emitObs(s.ObsFn, func(ev *ObsEvent_t) {
			ev.Type = ObsEventType_serverRoomOverLimit
			ev.RemoteAddr = sconn.remoteAddr
			ev.SessionId = sconn.sessionId
		})
		sconn.setObsCloseReason("roomOverLimit", "limit="+strconv.Itoa(maxRooms))
		sconn.conn.closer.Close2()
		return
	}
	room := s.roomMap[roomId]
	if room == nil {
		room = &room_t{
			id:        roomId,
			connSet:   map[*server_conn_t]struct{}{},
			RoomEpoch: zlibIdGen.NewId(),
		}
		s.roomMap[roomId] = room
	}
	room.connSet[sconn] = struct{}{}
	room.emptyTime = time.Time{}
	sconn.roomMap[roomId] = room
	changeSeq := room.ChangeSeq
	cVersionId := room.CVersionId
	roomEpoch := room.RoomEpoch
	s.roomMapLock.Unlock()
	sconn.sendMsgNoBlock(Msg_t{
		Cmd:        Cmd_roomValue,
		RoomEpoch:  roomEpoch,
		ChangeSeq:  changeSeq,
		RoomId:     roomId,
		CVersionId: cVersionId,
	})
}

// 实际把连接从房间移除.
func (sconn *server_conn_t) doRoomLeave(roomId string) {
	s := sconn.s
	s.roomMapLock.Lock()
	room, ok := sconn.roomMap[roomId]
	if !ok {
		s.roomMapLock.Unlock()
		return
	}
	delete(room.connSet, sconn)
	delete(sconn.roomMap, roomId)
	if len(room.connSet) == 0 {
		room.emptyTime = time.Now()
	}
	s.roomMapLock.Unlock()
}

// 发 Cmd_closeConn 要求客户端关闭连接. 同一连接多次只发一次. 发完起一个超时定时器,
// 客户端没在超时内断开则服务端主动关(防恶意/故障客户端继续占用资源).
func (sconn *server_conn_t) closeConn(isTemp bool, reason string) {
	s := sconn.s
	s.roomMapLock.Lock()
	already := sconn.hasSendAskClientStop
	sconn.hasSendAskClientStop = true
	s.roomMapLock.Unlock()
	if already {
		return
	}
	sconn.sendMsgNoBlock(Msg_t{Cmd: Cmd_closeConn, IsTemp: isTemp, Reason: reason})
	sconn.timerAskStopListenTimeout.SetFn(func() {
		_emitObs(s.ObsFn, func(ev *ObsEvent_t) {
			ev.Type = ObsEventType_serverCloseConnTimeout
			ev.RemoteAddr = sconn.remoteAddr
			ev.SessionId = sconn.sessionId
		})
		sconn.setObsCloseReason("closeConnTimeout", "")
		sconn.conn.closer.Close2()
	})
	sconn.timerAskStopListenTimeout.After(s.TimeoutCfg.Get().ServerAskStopListenTimeoutDur)
}

// 按 sessionId(网络层 sessionId 或 in-band identity)找到所有连接, 要求它们关闭.
// isTemp=true 客户端重连(重连重新过 OnAllowFn, 用于踢出/撤权); isTemp=false 客户端不再重连(永久封禁).
func (s *ServerManager) CloseConnBySessionId(sessionId string, isTemp bool, reason string) {
	s.sessionIdToConnListMapLock.Lock()
	connSet := s.sessionIdToConnListMap[sessionId]
	connList := make([]*server_conn_t, 0, len(connSet))
	for conn := range connSet {
		connList = append(connList, conn)
	}
	s.sessionIdToConnListMapLock.Unlock()
	for _, conn := range connList {
		conn.closeConn(isTemp, reason)
	}
}
