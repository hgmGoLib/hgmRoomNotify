// ReliableChat 例子: 用 hgmRoomNotify(ws 戳一下) + ajax(取真实数据) 实现
// "每条聊天消息可靠送达" 和 "离线消息/历史/回放/断线补发" 两个需求.
//
// 设计要点(也是这个例子想说明的对接方式):
//   - 真正的可靠性锚点是业务消息序号 MsgIndex(每个房间内从 1 单调递增, 存在数据库里).
//     ws 层的 ChangeSeq/RoomEpoch 只是 "戳一下" 的信号, 服务器重启会重置, 不作为可靠性依据.
//   - 服务端每来一条消息: 先写库(分配 MsgIndex), 再 FireChange, 把整条消息塞进 LiveData(尽力而为).
//   - 客户端收到 onChange:
//       * LiveData 有, 且正好是 lastIndex+1 这条 -> 直接用 LiveData 应用, 0 额外 RTT(快路径).
//       * 否则(LiveData 没有/被丢弃/超 1024/重连/服务器重启, 或者 跳号说明中间漏了) -> ajax 拉
//         lastIndex 之后的全部消息补齐(慢路径, 代价是一个 RTT).
//   - "是不是距离上次中断了" 不需要单独判断: 任何中断都会表现为 "LiveData 缺失" 或 "MsgIndex 跳号",
//     被上面的慢路径统一兜住.
//
// chatStore_t 就是这个例子里的 "数据库": 存每个房间的完整消息历史. 它独立于 ServerManager,
// 所以服务器(ws 层)重启不会丢消息, 重连后客户端能通过 ajax 把历史补回来.
package main

import "sync"

// 一条聊天消息. 字段名/JSON 字段名 ascii 一致.
type ChatMsg_t struct {
	RoomId   string
	MsgIndex uint64 // 该房间内单调递增的业务消息序号, 从 1 开始. 这是可靠送达的唯一依据.
	Sender   string
	Text     string
}

// 聊天消息存储(例子里的数据库). 协程安全. 独立于 ws 层, ws 重启不影响它.
type chatStore_t struct {
	lock  sync.Mutex
	rooms map[string][]ChatMsg_t // roomId -> 有序消息历史, 下标 i 对应 MsgIndex i+1.
}

// 追加一条消息, 分配 MsgIndex(= 当前条数+1), 返回写入后的完整消息.
func (s *chatStore_t) Post(roomId string, sender string, text string) ChatMsg_t {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.rooms == nil {
		s.rooms = map[string][]ChatMsg_t{}
	}
	list := s.rooms[roomId]
	m := ChatMsg_t{
		RoomId:   roomId,
		MsgIndex: uint64(len(list)) + 1,
		Sender:   sender,
		Text:     text,
	}
	s.rooms[roomId] = append(list, m)
	return m
}

// 返回 MsgIndex > afterIndex 的所有消息(即客户端 lastIndex 之后还没拿到的部分). 这是 ajax 拉取的后端.
func (s *chatStore_t) After(roomId string, afterIndex uint64) []ChatMsg_t {
	s.lock.Lock()
	defer s.lock.Unlock()
	list := s.rooms[roomId]
	if afterIndex >= uint64(len(list)) {
		return nil
	}
	out := make([]ChatMsg_t, len(list)-int(afterIndex))
	copy(out, list[afterIndex:])
	return out
}

// 返回房间当前消息总数(= 最大 MsgIndex). 仅测试/演示用于断言.
func (s *chatStore_t) Count(roomId string) int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return len(s.rooms[roomId])
}
