package hgmRoomNotify

import (
	"fmt"
	"sync/atomic"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibSync"
)

// 房间变化事件,传递给RoomEnter的回调.
type RoomOnChange_t struct {
	RoomId       string // 房间id.
	RoomEpoch    string // 房间纪元id. 每次房间被创建时生成. 不同的 RoomEpoch 表示房间被重建过(包括服务器重启).
	ChangeSeq    uint64 // 变化序号. 同一个 RoomEpoch 下递增表示有新变化.
	CVersionId   string // 自定义版本id. 服务器内存存储该数据. 调用者用于追踪实际数据变化. 最大100字节.
	LiveData     []byte // hgmBjson编码的实时数据. 长度为0表示本次没有传输. 只读,多个listener共享同一底层数组.
	ErrMsg       string // 回调中设置此字段表示报错. 非空时客户端进入 needManual 状态.
}

// 客户端加入房间. 本函数 不会报错,不会阻塞.
// 请注意不要向已经关闭的客户端发送 roomEnter, 此时该请求会被忽略.
func (c *Client) RoomEnter(roomId string,onChangeFn func(ev *RoomOnChange_t)) (leaveFn func()){
	if roomId==""{
		panic("hgmRoomNotify: RoomEnter roomId must not be empty")
	}
	if c.closerForTest.IsClose(){
		return func(){}
	}
	if c.authDenyFatalMsg.Get()!=""{
		// 已处于被服务端拒绝且未处理的致命状态(对接错误). RoomEnter 无效果.
		return func(){}
	}
	if c.connDenyLocal.Get(){
		// 当前连接被连接级拒绝(已注册 OnDenyFn). 进入房间本地直接回 OnDenyFn, 不找服务端.
		if c.OnDenyFn!=nil{
			c.OnDenyFn(&ClientDeny_t{IsConn:true, RoomId:roomId, Reason:"conn denied"})
		}
		return func(){}
	}
	thisListener:=&client_listener_t{
		onChangeFn: onChangeFn,
		c: c,
	}
	c.roomLock.Lock()
	if c.roomMap==nil{
		c.roomMap = map[string]*client_room_t{}
	}
	room,ok:=c.roomMap[roomId]
	if !ok{
		room=&client_room_t{
			listenerSet: map[*client_listener_t]struct{}{},
		}
		c.roomMap[roomId] = room
		c._roomIdReq_NOLOCK(roomId)
	}
	room.listenerSet[thisListener] = struct{}{}
	if ok{
		// 在锁内发送初始回调, 避免与 roomValue 的 onChangeAsync 竞争导致 pendingEv 被旧值覆盖.
		thisListener.onChangeAsync(RoomOnChange_t{
			RoomId:     roomId,
			RoomEpoch:  room.RoomEpoch,
			ChangeSeq:  room.ChangeSeq,
			CVersionId: room.CVersionId,
		})
	}
	c.roomLock.Unlock()
	c.noNeedIdleCloseTimer.Stop()
	c._init_afterEnter()
	return func(){
		c.roomLock.Lock()
		delete(room.listenerSet,thisListener)
		if len(room.listenerSet)==0 && c.roomMap[roomId]==room{
			delete(c.roomMap,roomId)
			c._roomIdReq_NOLOCK(roomId)
		}
		if len(c.roomMap)==0{
			c._noNeed__NOLOCK()
		}
		c.roomLock.Unlock()
	}
}

// 收到一次完整 roomValue(单条 Cmd_roomValue, 或分块重组后的结果)后, 更新房间状态并通知 listener.
// liveData 为本次完整的实时数据(可能为空).
func (c *Client) onRoomValue(roomId string, roomEpoch string, changeSeq uint64, cVersionId string, liveData []byte){
	c.roomLock.Lock()
	room,hasRoom:=c.roomMap[roomId]
	if hasRoom==false{
		c.roomLock.Unlock()
		return
	}
	if roomEpoch!=room.RoomEpoch{
		// 房间纪元变了(房间被重建或服务器重启), 无条件接受.
		room.RoomEpoch = roomEpoch
		room.ChangeSeq = changeSeq
		room.CVersionId = cVersionId
	}else if changeSeq>room.ChangeSeq{
		// 同纪元有新变化.
		room.ChangeSeq = changeSeq
		room.CVersionId = cVersionId
	}else{
		// 旧消息(竞争产生的), 整条忽略.
		c.roomLock.Unlock()
		return
	}
	ev:=RoomOnChange_t{
		RoomId:     roomId,
		RoomEpoch:  room.RoomEpoch,
		ChangeSeq:  room.ChangeSeq,
		CVersionId: room.CVersionId,
		LiveData:   liveData,
	}
	for listener:=range room.listenerSet{
		listener.onChangeAsync(ev)
	}
	c.roomLock.Unlock()
}

type client_room_t struct{
	RoomEpoch    string // 房间纪元id. 用于检测房间重建.
	ChangeSeq    uint64 // 变化序号. 同一个RoomEpoch下递增表示有新变化.
	CVersionId   string // 自定义版本id. 当前房间的CVersionId.
	listenerSet map[*client_listener_t]struct{}
}
type client_listener_t struct {
	onChangeFn func(ev *RoomOnChange_t)
	singleUnd zlibSync.SingleUnd
	pendingEv atomic.Pointer[RoomOnChange_t]
	c *Client
}
// 异步通知 listener 房间变化. 使用 pendingEv + singleUnd 实现合并:
// 多次快速调用只执行最新一次(旧的被覆盖). singleUnd.Do 的语义是 "排队再执行一次"(非tryLock),
// 所以不会丢失: 如果 Do 在 fn 执行期间被调用, fn 执行完后会再跑一次, 此时 pendingEv 已更新为最新值.
func (listener *client_listener_t) onChangeAsync(ev RoomOnChange_t){
	evCopy:=ev
	listener.pendingEv.Store(&evCopy)
	listener.singleUnd.Do(func() {
		listener.c.onChangeRunningCount.Add(1)
		defer listener.c.onChangeRunningCount.Add(^uint32(0))
		ev2:=listener.pendingEv.Load()
		if ev2==nil{
			return
		}
		func(){
			defer func(){
				r:=recover()
				if r!=nil{
					listener.c.setNeedManual("onChangeFn panic: "+fmt.Sprint(r))
				}
			}()
			listener.onChangeFn(ev2)
		}()
		if ev2.ErrMsg!=""{
			listener.c.setNeedManual("onChangeFn ErrMsg: "+ev2.ErrMsg)
		}
	})
}
func (c *Client) _roomIdReq_NOLOCK(roomId string){
	if c.roomIdReqSet ==nil{
		c.roomIdReqSet = map[string]struct{}{}
	}
	c.roomIdReqSet[roomId] = struct{}{}
	c.roomNl.NotifyAll()
}

func (c *Client) _noNeed__NOLOCK(){
	c.noNeedIdleCloseTimer.SetFn(func() {
		c.roomLock.Lock()
		if len(c.roomMap)>0{
			// 定时器触发时已经有新的需求了, 不需要关闭连接.
			c.roomLock.Unlock()
			return
		}
		c.roomLock.Unlock()
		thisConn:=c.thisConn.Load()
		if thisConn!=nil{
			c.logClose(CloseReason_clientNoNeed,"")
			thisConn.conn.closer.Close()
		}
		c.roomLock.Lock()
		c.roomNl.NotifyAll()
		c.roomLock.Unlock()
	})
	c.noNeedIdleCloseTimer.After(c.TimeoutCfg.Get().ClientNoNeedIdleDur)
}
func (c *Client) HasNeed()bool{
	if c.closerForTest.IsClose(){
		return false
	}
	c.roomLock.Lock()
	out:= len(c.roomMap)>0
	c.roomLock.Unlock()
	return out
}
