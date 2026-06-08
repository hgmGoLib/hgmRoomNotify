package hgmRoomNotify

import "time"

// 面向终端用户的ui状态. 用于在界面上展示当前数据同步状态.
// "现在距离上次收到服务端确认连接有效的时间" 是客户端对象上的跨连接字段(lastReadSuccTimeAll),
// 收到任意一个能证明当前ws应用层链路活着的服务端入站消息时更新.
type UiStatusToUser_t = string

// 已同步: 最近收到服务端有效消息(在 ClientLastReadToReconnectDur 内),
// 且当前没有房间变化回调正在运行, 且ws连接正常. 表示数据是最新的.
const UiStatusToUser_synced UiStatusToUser_t = "synced"
// 同步中: 最近收到服务端有效消息(在 ClientLastReadToUiNoWorkDur 内),
// 但存在以下任一情况: 距离上次收到有效消息已超过 ClientLastReadToReconnectDur,
// 或当前有房间变化回调正在运行, 或ws连接不正常(断开/重连中).
// 预期网络大概率正常, 可以自动恢复到已同步.
const UiStatusToUser_syncing UiStatusToUser_t = "syncing"
// 离线: 距离上次收到服务端有效消息已超过 ClientLastReadToUiNoWorkDur.
// 网络大概率不正常, 用户应检查网络. 网络恢复后可自动回到已同步.
const UiStatusToUser_offline UiStatusToUser_t = "offline"
// 需要手动: 存在无法自动恢复的问题, 需要用户手动介入(比如刷新页面).
// 触发条件: 开发者传入的 onChangeFn 回调报错(通过 ErrMsg 或 panic/throw),
// 或 isStopListen 为 true(服务端发送 Cmd_askStopListen 或外部调用 SetIsStopListen(true)).
const UiStatusToUser_needManual UiStatusToUser_t = "needManual"

// 获取当前面向终端用户的ui状态.
// 判断优先级: needManual > offline > syncing > synced.
func (c *Client) GetUiStatusToUser() UiStatusToUser_t{
	if c.needManualMsg.Get()!=""{
		return UiStatusToUser_needManual
	}
	if c.isStopListen.Get(){
		return UiStatusToUser_needManual
	}
	cfg:=c.TimeoutCfg.Get()
	sinceLastRead:=time.Since(c.lastReadSuccTimeAll.Get())
	if sinceLastRead>=cfg.ClientLastReadToUiNoWorkDur{
		return UiStatusToUser_offline
	}
	isWsConnected:=c.IsConnectedSucc()
	if sinceLastRead>=cfg.ClientLastReadToReconnectDur || c.onChangeRunningCount.Load()>0 || !isWsConnected{
		return UiStatusToUser_syncing
	}
	return UiStatusToUser_synced
}

// 获取距离上次收到服务端有效消息的时间. 从未收到过时返回 -1.
func (c *Client) GetSinceLastServerConfirm() time.Duration{
	t:=c.lastReadSuccTimeAll.Get()
	if t.IsZero(){
		return -1
	}
	return time.Since(t)
}

// 获取 needManual 状态的原因文本. 空字符串表示不在 needManual 状态.
func (c *Client) GetNeedManualMsg() string{
	return c.needManualMsg.Get()
}

// 获取当前订阅的房间数量.
func (c *Client) GetRoomCount() int{
	c.roomLock.Lock()
	n:=len(c.roomMap)
	c.roomLock.Unlock()
	return n
}

// 设置客户端进入 needManual 状态.
func (c *Client) setNeedManual(msg string){
	c.needManualMsg.Set(msg)
	_emitObs(c.ObsFn, func(ev *ObsEvent_t) {
		ev.Type = ObsEventType_clientNeedManual
		ev.CloseDetail = msg
	})
}
