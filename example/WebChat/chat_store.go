// WebChat 例子: 浏览器(React) + golang 后端的可靠聊天室.
// 可靠传输沿用 example/ReliableChat 的模式: ws 只"戳一下"通知有变化, 真实消息数据和可靠性靠
// 业务消息序号 MsgIndex(每房间单调递增, 存内存库) + ajax 兜底. 服务端发消息时把整条消息塞进
// LiveData 当快路径(省一个 RTT); 客户端 LiveData 缺失/跳号就 ajax 拉 lastIndex 之后的全部补齐.
//
// 这个文件是例子里的"数据库": 纯内存, 协程安全, 独立于 ws 层.
package webchat

import "sync"

// 一条聊天消息. 字段名/JSON 字段名 ascii 一致(前后端共用).
type ChatMsg_t struct {
	RoomId   string
	MsgIndex uint64 // 该房间内单调递增的业务消息序号, 从 1 开始. 这是可靠送达的唯一依据.
	Sender   string
	Text     string
}

// 聊天消息存储(例子里的内存数据库). 协程安全. 独立于 ws 层, ws 重启不影响它.
type ChatStore_t struct {
	lock  sync.Mutex
	rooms map[string][]ChatMsg_t // roomId -> 有序消息历史, 下标 i 对应 MsgIndex i+1.
}

// 追加一条消息, 分配 MsgIndex(= 当前条数+1), 返回写入后的完整消息.
func (s *ChatStore_t) Post(roomId string, sender string, text string) ChatMsg_t {
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
func (s *ChatStore_t) After(roomId string, afterIndex uint64) []ChatMsg_t {
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
