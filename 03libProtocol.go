package hgmRoomNotify

import (
	"encoding/binary"
	"strconv"
	"sync"
	"time"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibBytes"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibCloser"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibMath"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibVnet"
	"math/bits"
)
type TimeoutCfg_t struct {
	ClientReconnectMinDur        time.Duration // fieldId=1. 客户端 最小重连时间间隔。比如 1秒。
	ClientIdleToSendKeepAliveDur time.Duration // fieldId=2. 客户端 网络idle（最后发包时间/最后收包时间的较小值）到 发送keep alive 的时间间隔。比如1秒。
	ClientLastReadToReconnectDur time.Duration // fieldId=3. 客户端 最后收包时间 到关闭连接 开始重连的时间间隔。比如 5秒。
	ClientWsDialTimeoutDur       time.Duration // fieldId=4. 客户端 websocket.Dial 这个步骤的 最大等待时间间隔。比如 2秒。 （注意 这个步骤有个tcp.Dial+http req/resp ws最少:2rtt, wss最少:3rtt）
	ClientNoNeedIdleDur time.Duration // fieldId=5. 客户端没有需求,到 关闭连接的超时.
	ClientLastReadToUiNoWorkDur time.Duration // fieldId=6. 客户端 最后收包时间 超过此值 ui状态显示为离线. 默认10秒.
	ServerLastReadToCloseDur     time.Duration // 不参与二进制序列化(仅服务端使用). 服务端 最后收包时间 到关闭连接 的时间间隔。比如 2分钟。
	ServerAuthTimeoutDur         time.Duration // 不参与二进制序列化(仅服务端使用). 服务端 未认证连接的超时关闭时间间隔。比如 30秒。
	ServerAskStopListenTimeoutDur time.Duration // 不参与二进制序列化(仅服务端使用). 服务端 发送askStopListen后等待客户端断开的超时时间间隔。比如 30秒。
}

// Cmd_setTimeCfg 序列化的KV字段数量.
const timeoutCfgKvFieldCount = 6

type Cmd_t = uint8
const Cmd_ping Cmd_t = 1
const Cmd_setTimeCfg Cmd_t = 2
const Cmd_roomEnter Cmd_t = 3
const Cmd_roomLeave Cmd_t = 4
const Cmd_roomValue Cmd_t = 5
const Cmd_identity Cmd_t = 6   // 客户端->服务端. 连接后首包. 携带 opaque identity 字符串. 重连重发.
const Cmd_connAllow Cmd_t = 7  // 服务端->客户端. 连接已批准. 携带 AuthEnabled(服务端是否启用认证).
const Cmd_deny Cmd_t = 8       // 服务端->客户端. 拒绝(连接或房间). DenyScope+RoomId+Reason.
const Cmd_closeConn Cmd_t = 9  // 服务端->客户端. 要求客户端关闭连接. IsTemp+Reason.

// Cmd_deny 的 DenyScope 取值.
type DenyScope_t = uint8
const DenyScope_conn DenyScope_t = 1 // 连接级拒绝(identity 认证失败).
const DenyScope_room DenyScope_t = 2 // 房间级拒绝(进入某房间被拒).

// 协议消息. 按照Cmd选择有效字段进行序列化.
// Cmd_ping: 无额外字段.
// Cmd_setTimeCfg: TimeoutCfg 有效.
// Cmd_roomEnter: RoomId 有效.
// Cmd_roomLeave: RoomId 有效.
// Cmd_roomValue: RoomId, RoomEpoch, ChangeSeq, CVersionId, LiveData 有效.
// Cmd_identity: Identity 有效.
// Cmd_connAllow: AuthEnabled 有效.
// Cmd_deny: DenyScope, RoomId(房间级时), Reason 有效.
// Cmd_closeConn: IsTemp, Reason 有效.
type Msg_t struct {
	Cmd          Cmd_t
	RoomId       string
	RoomEpoch    string // 房间纪元id. 每次房间被创建时由 zlibIdGen.NewId() 生成. 用于检测房间被重建(包括服务器重启).
	ChangeSeq    uint64 // 变化序号. 同一个 RoomEpoch 下递增表示有新变化.
	CVersionId   string // 自定义版本id. 服务器内存存储该数据. 调用者用于追踪实际数据变化. 最大100字节.
	LiveData     []byte // 事件发生时的附加实时数据. 本模块不存储. 最大1024字节.
	TimeoutCfg   *TimeoutCfg_t
	Identity     string    // Cmd_identity. opaque 凭证. 最大65535字节.
	AuthEnabled  bool      // Cmd_connAllow. 服务端是否配置了认证(用于客户端"漏接 onDenyFn 当场告警").
	DenyScope    DenyScope_t // Cmd_deny. 拒绝的作用域.
	Reason       string    // Cmd_deny / Cmd_closeConn. 原因文本. 最大65535字节.
	IsTemp       bool      // Cmd_closeConn. true=临时(客户端应重连); false=永久(客户端不再重连).
}

// 按Cmd选择字段的二进制序列化格式(小端法):
// Cmd_ping:          [Cmd: uint8]
// Cmd_setTimeCfg:    [Cmd: uint8][count: uint8][ [fieldId: uint8][value: int64LE] ] * count (fieldId见TimeoutCfg_t注释, 不认识的fieldId跳过)
// Cmd_roomEnter:     [Cmd: uint8][RoomId: uint16LE长度 + 内容]
// Cmd_roomLeave:     [Cmd: uint8][RoomId: uint16LE长度 + 内容]
// Cmd_roomValue:     [Cmd: uint8][RoomId: uint16LE长度 + 内容][RoomEpoch: uint8长度 + 内容][ChangeSeq: uvarint][CVersionId: uint8长度 + 内容][LiveData: uint16LE长度 + 内容]
// Cmd_identity:      [Cmd: uint8][Identity: uint16LE长度 + 内容]
// Cmd_connAllow:     [Cmd: uint8][AuthEnabled: uint8]
// Cmd_deny:          [Cmd: uint8][DenyScope: uint8][RoomId: uint16LE长度 + 内容][Reason: uint16LE长度 + 内容]
// Cmd_closeConn:     [Cmd: uint8][IsTemp: uint8][Reason: uint16LE长度 + 内容]
func (msg *Msg_t) MarshalBinary() ([]byte, string) {
	size, errMsg := msg.BinarySize()
	if errMsg != "" {
		return nil, errMsg
	}
	buf := make([]byte, size)
	msg.MarshalBinaryInto(buf)
	return buf, ""
}

// 计算序列化后的字节数, 同时校验输入合法性. errMsg 非空表示输入无法序列化.
func (msg *Msg_t) BinarySize() (size int, errMsg string) {
	switch msg.Cmd {
	case Cmd_ping:
		return 1, ""
	case Cmd_connAllow:
		return 1 + 1, ""
	case Cmd_identity:
		if len(msg.Identity) > 65535 {
			return 0, "Identity too long, max 65535 bytes, got " + strconv.Itoa(len(msg.Identity))
		}
		return 1 + (2 + len(msg.Identity)), ""
	case Cmd_deny:
		if len(msg.RoomId) > 65535 {
			return 0, "RoomId too long, max 65535 bytes, got " + strconv.Itoa(len(msg.RoomId))
		}
		if len(msg.Reason) > 65535 {
			return 0, "Reason too long, max 65535 bytes, got " + strconv.Itoa(len(msg.Reason))
		}
		return 1 + 1 + (2 + len(msg.RoomId)) + (2 + len(msg.Reason)), ""
	case Cmd_closeConn:
		if len(msg.Reason) > 65535 {
			return 0, "Reason too long, max 65535 bytes, got " + strconv.Itoa(len(msg.Reason))
		}
		return 1 + 1 + (2 + len(msg.Reason)), ""
	case Cmd_setTimeCfg:
		if msg.TimeoutCfg == nil {
			return 0, "TimeoutCfg is nil"
		}
		return 1 + 1 + timeoutCfgKvFieldCount*9, ""
	case Cmd_roomEnter, Cmd_roomLeave:
		if len(msg.RoomId) > 65535 {
			return 0, "RoomId too long, max 65535 bytes, got " + strconv.Itoa(len(msg.RoomId))
		}
		return 1 + (2+len(msg.RoomId)), ""
	case Cmd_roomValue:
		if len(msg.RoomId) > 65535 {
			return 0, "RoomId too long, max 65535 bytes, got " + strconv.Itoa(len(msg.RoomId))
		}
		if len(msg.RoomEpoch) > 255 {
			return 0, "RoomEpoch too long, max 255 bytes, got " + strconv.Itoa(len(msg.RoomEpoch))
		}
		if len(msg.CVersionId) > 255 {
			return 0, "CVersionId too long, max 255 bytes, got " + strconv.Itoa(len(msg.CVersionId))
		}
		if len(msg.LiveData) > 65535 {
			return 0, "LiveData too long, max 65535 bytes, got " + strconv.Itoa(len(msg.LiveData))
		}
		return 1 + (2+len(msg.RoomId)) + (1+len(msg.RoomEpoch)) + getUvarintOutputSize(msg.ChangeSeq) + (1+len(msg.CVersionId)) + (2+len(msg.LiveData)), ""
	default:
		return 0, "unknown cmd " + strconv.Itoa(int(msg.Cmd))
	}
}

// 将消息序列化写入预分配的字节切片. 调用者保证 len(buf) >= BinarySize().
func (msg *Msg_t) MarshalBinaryInto(buf []byte) {
	buf[0] = msg.Cmd
	switch msg.Cmd {
	case Cmd_ping:
	case Cmd_connAllow:
		if msg.AuthEnabled {
			buf[1] = 1
		} else {
			buf[1] = 0
		}
	case Cmd_identity:
		pos := 1
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(msg.Identity))); pos += 2
		copy(buf[pos:], msg.Identity)
	case Cmd_deny:
		pos := 1
		buf[pos] = msg.DenyScope; pos++
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(msg.RoomId))); pos += 2
		pos += copy(buf[pos:], msg.RoomId)
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(msg.Reason))); pos += 2
		copy(buf[pos:], msg.Reason)
	case Cmd_closeConn:
		pos := 1
		if msg.IsTemp {
			buf[pos] = 1
		} else {
			buf[pos] = 0
		}
		pos++
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(msg.Reason))); pos += 2
		copy(buf[pos:], msg.Reason)
	case Cmd_setTimeCfg:
		pos := 1
		buf[pos] = timeoutCfgKvFieldCount; pos++
		buf[pos] = 1; pos++; binary.LittleEndian.PutUint64(buf[pos:], uint64(msg.TimeoutCfg.ClientReconnectMinDur)); pos += 8
		buf[pos] = 2; pos++; binary.LittleEndian.PutUint64(buf[pos:], uint64(msg.TimeoutCfg.ClientIdleToSendKeepAliveDur)); pos += 8
		buf[pos] = 3; pos++; binary.LittleEndian.PutUint64(buf[pos:], uint64(msg.TimeoutCfg.ClientLastReadToReconnectDur)); pos += 8
		buf[pos] = 4; pos++; binary.LittleEndian.PutUint64(buf[pos:], uint64(msg.TimeoutCfg.ClientWsDialTimeoutDur)); pos += 8
		buf[pos] = 5; pos++; binary.LittleEndian.PutUint64(buf[pos:], uint64(msg.TimeoutCfg.ClientNoNeedIdleDur)); pos += 8
		buf[pos] = 6; pos++; binary.LittleEndian.PutUint64(buf[pos:], uint64(msg.TimeoutCfg.ClientLastReadToUiNoWorkDur))
	case Cmd_roomEnter, Cmd_roomLeave:
		pos := 1
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(msg.RoomId))); pos += 2
		copy(buf[pos:], msg.RoomId)
	case Cmd_roomValue:
		pos := 1
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(msg.RoomId))); pos += 2
		pos += copy(buf[pos:], msg.RoomId)
		buf[pos] = uint8(len(msg.RoomEpoch)); pos++
		pos += copy(buf[pos:], msg.RoomEpoch)
		pos += binary.PutUvarint(buf[pos:], msg.ChangeSeq)
		buf[pos] = uint8(len(msg.CVersionId)); pos++
		pos += copy(buf[pos:], msg.CVersionId)
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(msg.LiveData))); pos += 2
		copy(buf[pos:], msg.LiveData)
	}
}

// 将消息序列化写入 BufWriter. 格式与 MarshalBinaryInto 一致.
func (msg *Msg_t) MarshalBinaryTo(w *zlibBytes.BufWriter) {
	w.WriteByte_(msg.Cmd)
	switch msg.Cmd {
	case Cmd_ping:
	case Cmd_connAllow:
		if msg.AuthEnabled {
			w.WriteByte_(1)
		} else {
			w.WriteByte_(0)
		}
	case Cmd_identity:
		w.WriteLittleEndUint16(uint16(len(msg.Identity)))
		w.WriteString_(msg.Identity)
	case Cmd_deny:
		w.WriteByte_(msg.DenyScope)
		w.WriteLittleEndUint16(uint16(len(msg.RoomId)))
		w.WriteString_(msg.RoomId)
		w.WriteLittleEndUint16(uint16(len(msg.Reason)))
		w.WriteString_(msg.Reason)
	case Cmd_closeConn:
		if msg.IsTemp {
			w.WriteByte_(1)
		} else {
			w.WriteByte_(0)
		}
		w.WriteLittleEndUint16(uint16(len(msg.Reason)))
		w.WriteString_(msg.Reason)
	case Cmd_setTimeCfg:
		w.WriteByte_(timeoutCfgKvFieldCount)
		w.WriteByte_(1); w.WriteLittleEndUint64(uint64(msg.TimeoutCfg.ClientReconnectMinDur))
		w.WriteByte_(2); w.WriteLittleEndUint64(uint64(msg.TimeoutCfg.ClientIdleToSendKeepAliveDur))
		w.WriteByte_(3); w.WriteLittleEndUint64(uint64(msg.TimeoutCfg.ClientLastReadToReconnectDur))
		w.WriteByte_(4); w.WriteLittleEndUint64(uint64(msg.TimeoutCfg.ClientWsDialTimeoutDur))
		w.WriteByte_(5); w.WriteLittleEndUint64(uint64(msg.TimeoutCfg.ClientNoNeedIdleDur))
		w.WriteByte_(6); w.WriteLittleEndUint64(uint64(msg.TimeoutCfg.ClientLastReadToUiNoWorkDur))
	case Cmd_roomEnter, Cmd_roomLeave:
		w.WriteLittleEndUint16(uint16(len(msg.RoomId)))
		w.WriteString_(msg.RoomId)
	case Cmd_roomValue:
		w.WriteLittleEndUint16(uint16(len(msg.RoomId)))
		w.WriteString_(msg.RoomId)
		w.WriteByte_(uint8(len(msg.RoomEpoch)))
		w.WriteString_(msg.RoomEpoch)
		w.WriteUvarint(msg.ChangeSeq)
		w.WriteByte_(uint8(len(msg.CVersionId)))
		w.WriteString_(msg.CVersionId)
		w.WriteLittleEndUint16(uint16(len(msg.LiveData)))
		w.Write_(msg.LiveData)
	}
}

// 从二进制数据反序列化Msg_t. 按Cmd选择字段解析.
func UnmarshalMsg(data []byte) (msg Msg_t, errMsg string) {
	if len(data) < 1 {
		return msg, "data too short"
	}
	msg.Cmd = data[0]
	pos := 1

	switch msg.Cmd {
	case Cmd_ping:
		return msg, ""
	case Cmd_connAllow:
		if pos+1 > len(data) {
			return msg, "data too short for connAllow"
		}
		msg.AuthEnabled = data[pos] != 0
		return msg, ""
	case Cmd_identity:
		if pos+2 > len(data) {
			return msg, "data too short for identity len"
		}
		idLen := int(binary.LittleEndian.Uint16(data[pos:])); pos += 2
		if pos+idLen > len(data) {
			return msg, "identity overflow"
		}
		if idLen > 0 {
			msg.Identity = string(data[pos : pos+idLen])
		}
		return msg, ""
	case Cmd_deny:
		if pos+1 > len(data) {
			return msg, "data too short for denyScope"
		}
		msg.DenyScope = data[pos]; pos++
		if pos+2 > len(data) {
			return msg, "data too short for deny roomId len"
		}
		roomIdLen := int(binary.LittleEndian.Uint16(data[pos:])); pos += 2
		if pos+roomIdLen > len(data) {
			return msg, "deny roomId overflow"
		}
		if roomIdLen > 0 {
			msg.RoomId = string(data[pos : pos+roomIdLen]); pos += roomIdLen
		}
		if pos+2 > len(data) {
			return msg, "data too short for deny reason len"
		}
		reasonLen := int(binary.LittleEndian.Uint16(data[pos:])); pos += 2
		if pos+reasonLen > len(data) {
			return msg, "deny reason overflow"
		}
		if reasonLen > 0 {
			msg.Reason = string(data[pos : pos+reasonLen])
		}
		return msg, ""
	case Cmd_closeConn:
		if pos+1 > len(data) {
			return msg, "data too short for closeConn isTemp"
		}
		msg.IsTemp = data[pos] != 0; pos++
		if pos+2 > len(data) {
			return msg, "data too short for closeConn reason len"
		}
		reasonLen := int(binary.LittleEndian.Uint16(data[pos:])); pos += 2
		if pos+reasonLen > len(data) {
			return msg, "closeConn reason overflow"
		}
		if reasonLen > 0 {
			msg.Reason = string(data[pos : pos+reasonLen])
		}
		return msg, ""
	case Cmd_setTimeCfg:
		if pos+1 > len(data) {
			return msg, "data too short for timeoutCfg count"
		}
		count := int(data[pos]); pos++
		if pos+count*9 > len(data) {
			return msg, "data too short for timeoutCfg kv pairs"
		}
		cfg := &TimeoutCfg_t{}
		for i := 0; i < count; i++ {
			fieldId := data[pos]; pos++
			val := time.Duration(binary.LittleEndian.Uint64(data[pos:])); pos += 8
			switch fieldId {
			case 1: cfg.ClientReconnectMinDur = val
			case 2: cfg.ClientIdleToSendKeepAliveDur = val
			case 3: cfg.ClientLastReadToReconnectDur = val
			case 4: cfg.ClientWsDialTimeoutDur = val
			case 5: cfg.ClientNoNeedIdleDur = val
			case 6: cfg.ClientLastReadToUiNoWorkDur = val
			// 不认识的fieldId跳过, 保持前向兼容.
			}
		}
		msg.TimeoutCfg = cfg
		return msg, ""
	case Cmd_roomEnter, Cmd_roomLeave:
		if pos+2 > len(data) {
			return msg, "data too short for roomId len"
		}
		roomIdLen := int(binary.LittleEndian.Uint16(data[pos:])); pos += 2
		if pos+roomIdLen > len(data) {
			return msg, "roomId overflow"
		}
		if roomIdLen > 0 {
			msg.RoomId = string(data[pos : pos+roomIdLen])
		}
		return msg, ""
	case Cmd_roomValue:
		if pos+2 > len(data) {
			return msg, "data too short for roomId len"
		}
		roomIdLen := int(binary.LittleEndian.Uint16(data[pos:])); pos += 2
		if pos+roomIdLen > len(data) {
			return msg, "roomId overflow"
		}
		if roomIdLen > 0 {
			msg.RoomId = string(data[pos : pos+roomIdLen]); pos += roomIdLen
		}

		if pos+1 > len(data) {
			return msg, "data too short for RoomEpoch len"
		}
		roomEpochLen := int(data[pos]); pos++
		if pos+roomEpochLen > len(data) {
			return msg, "RoomEpoch overflow"
		}
		if roomEpochLen > 0 {
			msg.RoomEpoch = string(data[pos : pos+roomEpochLen]); pos += roomEpochLen
		}

		changeSeq, uvarintLen := binary.Uvarint(data[pos:])
		if uvarintLen <= 0 {
			return msg, "ChangeSeq uvarint decode fail"
		}
		msg.ChangeSeq = changeSeq
		pos += uvarintLen

		if pos+1 > len(data) {
			return msg, "data too short for CVersionId len"
		}
		cVersionIdLen := int(data[pos]); pos++
		if pos+cVersionIdLen > len(data) {
			return msg, "CVersionId overflow"
		}
		if cVersionIdLen > 0 {
			msg.CVersionId = string(data[pos : pos+cVersionIdLen]); pos += cVersionIdLen
		}

		if pos+2 > len(data) {
			return msg, "data too short for data len"
		}
		dataLen := int(binary.LittleEndian.Uint16(data[pos:])); pos += 2
		if pos+dataLen > len(data) {
			return msg, "data field overflow"
		}
		if dataLen > 0 {
			msg.LiveData = make([]byte, dataLen)
			copy(msg.LiveData, data[pos:pos+dataLen])
		}
		return msg, ""
	default:
		return msg, "unknown cmd"
	}
}

// 负责所有协议及序列化逻辑.
// 不负责 连接管理/超时/关闭/如何使用读写数据之类 的需求
type conn_frame_t struct{
	onReadFinishSucc func() // require
	onWriteFinishSucc func()
	onReadMsg func(msg Msg_t)
	onLogClose    func(LastCloseReason CloseReason_t,log string)

	raw              zlibVnet.Frame16Conn_i
	closer           zlibCloser.Closer
	writeLock        sync.Mutex
	// writeMsg 复用的发送缓冲与 FrameBuf(均在 writeLock 内使用). 复用避免每次发包都新建 BufWriter
	// 和 &fb 逃逸分配; 配合前置留空(prefix)让 WriteFrame 原地写 websocket 帧头, 零 payload copy.
	writeBufW        zlibBytes.BufWriter
	writeFb          zlibVnet.FrameBuf
}

// 一个 frame(websocket message) 内可以包含多个Msg_t, 格式: [uint16 len][msg bytes][uint16 len][msg bytes]...
func (c *conn_frame_t) readThread(){
	var fb zlibVnet.FrameBuf
	for {
		err:=c.raw.ReadFrame(&fb)
		if c.closer.IsClose(){
			return
		}
		if err!=nil{
			if c.onLogClose!=nil { c.onLogClose(CloseReason_readFail,"fail1 "+err.Error()) }
			c.closer.Close()
			return
		}
		if len(fb.Buf)-int(fb.StartPos)==0{
			if c.onLogClose!=nil {c.onLogClose(CloseReason_protocolNoMatch,"Read protocol not match len(buf)==0")}
			c.closer.Close()
			return
		}
		if c.onReadFinishSucc !=nil{
			c.onReadFinishSucc()
		}
		if c.onReadMsg!=nil{
			data:=fb.Buf[fb.StartPos:]
			for len(data)>0{
				if len(data)<2{
					if c.onLogClose!=nil {c.onLogClose(CloseReason_protocolNoMatch,"multi-msg frame: incomplete length header, remaining="+strconv.Itoa(len(data)))}
					c.closer.Close()
					return
				}
				frameLen:=int(binary.LittleEndian.Uint16(data[:2]))
				data=data[2:]
				if frameLen==0 || len(data)<frameLen{
					if c.onLogClose!=nil {c.onLogClose(CloseReason_protocolNoMatch,"multi-msg frame: bad body, frameLen="+strconv.Itoa(frameLen)+" remaining="+strconv.Itoa(len(data)))}
					c.closer.Close()
					return
				}
				msg,errMsg2:=UnmarshalMsg(data[:frameLen])
				if errMsg2!=""{
					if c.onLogClose!=nil {c.onLogClose(CloseReason_protocolNoMatch,"Read protocol not match UnmarshalMsg "+errMsg2)}
					c.closer.Close()
					return
				}
				data=data[frameLen:]
				c.onReadMsg(msg)
				if c.closer.IsClose(){
					return
				}
			}
		}
	}
}
// 序列化并发送单条消息, 带uint16长度前缀. 客户端使用.
// 注意: uint16(msgSize) 不会溢出, 因为客户端只发送 ping(1字节)/roomEnter/roomLeave(最大1027字节),
// 远小于 uint16 最大值 65535.
func (c *conn_frame_t) writeMsg(msg Msg_t) {
	msgSize, errMsg:=msg.BinarySize()
	if errMsg!=""{
		if c.onLogClose!=nil { c.onLogClose(CloseReason_protocolNoMatch,"writeMsg BinarySize "+errMsg) }
		c.closer.Close2()
		return
	}
	prefix:=int(c.raw.GetFrameBufPrefixPreservedSize())
	c.writeLock.Lock()
	c.writeBufW.Reset()
	// 前面留出 prefix 字节给 websocket 帧头(WriteFrame 原地写头, 零 copy), 子帧 [uint16 len][msg] 从 prefix 处开始.
	c.writeBufW.AddPos(prefix)
	c.writeBufW.WriteLittleEndUint16(uint16(msgSize))
	msg.MarshalBinaryTo(&c.writeBufW)
	c.writeBuf__NOLOCK(c.writeBufW.GetBytes(),prefix)
	c.writeLock.Unlock()
}

func (c *conn_frame_t) writeBuf__NOLOCK(buf []byte,startPos int) {
	if c.closer.IsClose(){
		return
	}
	c.writeFb.Buf = buf
	c.writeFb.StartPos = uint16(startPos)
	err:=c.raw.WriteFrame(&c.writeFb)
	if err!=nil{
		if c.closer.IsClose(){
			return
		}
		if c.onLogClose!=nil {c.onLogClose(CloseReason_writeFail,"fail1 "+err.Error())}
		c.closer.Close2()
		return
	}
	if c.onWriteFinishSucc !=nil{
		c.onWriteFinishSucc()
	}
	return
}
func getUvarintOutputSize(x uint64) int {
	return (bits.Len64(x | 127) + 6) / 7
}
func (tc *TimeoutCfg_t) InitWithDefault() {
	const minTime = time.Millisecond*100
	zlibMath.InitDefaultMin(&tc.ClientReconnectMinDur,5*time.Second,minTime)
	zlibMath.InitDefaultMin(&tc.ClientIdleToSendKeepAliveDur,time.Duration(4.5*float64(time.Second)),minTime)
	zlibMath.InitDefaultMin(&tc.ClientLastReadToReconnectDur,10*time.Second,minTime)
	zlibMath.InitDefaultMin(&tc.ClientWsDialTimeoutDur,time.Second*5,minTime)
	zlibMath.InitDefaultMin(&tc.ClientNoNeedIdleDur,time.Second*20,minTime)
	zlibMath.InitDefaultMin(&tc.ClientLastReadToUiNoWorkDur,time.Second*10,minTime)
	zlibMath.InitDefaultMin(&tc.ServerLastReadToCloseDur,time.Second*120,minTime)
	zlibMath.InitDefaultMin(&tc.ServerAuthTimeoutDur,time.Second*30,minTime)
	zlibMath.InitDefaultMin(&tc.ServerAskStopListenTimeoutDur,time.Second*30,minTime)
	// 强制 keepalive 间隔的两倍 不大于 读超时, 否则 ping 节奏跟不上读超时, 健康连接会被误关.
	if tc.ClientIdleToSendKeepAliveDur*2 > tc.ClientLastReadToReconnectDur {
		tc.ClientIdleToSendKeepAliveDur = tc.ClientLastReadToReconnectDur / 2
	}
	return
}
