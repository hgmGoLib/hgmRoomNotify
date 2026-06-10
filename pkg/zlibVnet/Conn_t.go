package zlibVnet

// 最底层把一帧序列化完成后真正写到 socket 的那整段字节(含下层自己追加的 prefix/suffix 帧头/帧尾)的硬上限, 单位字节.
// 含义: frameBuf 里最终交给 socket 的 buf 整段长度 <= Frame16MaxWriteSize(= 64KB). 这是协议隐式恒定值,
// 下层不能控制/放大它; 上层可用 payload = Frame16MaxWriteSize - Prefix - Suffix.
// 发送方需要更小帧时可在 <= 此值内自行设更小的发送上限(连接建立后固定), 接收方固定按此值拒收超限帧.
const Frame16MaxWriteSize = 64 * 1024

// 下层在 payload 前后各自需要追加的字节数(同一底层实现对象该值固定不变).
// 上层据此给 buffer 故意留出前后空白, 让下层原地写帧头/帧尾, 实现 0 alloc / 0 copy.
type FrameBufPreservedSize_t struct {
	Prefix uint16
	Suffix uint16
}

// 分层帧连接接口.
// frame16 指的是序列化为 [len uint16][data []byte] [len uint16][data []byte] 这种模式.
// 底层最终写入(含 prefix/suffix)硬限 Frame16MaxWriteSize; 上层可用 payload 随包裹越来越小(类似 IP MTU).
type Frame16Conn_i interface {
	// 下层在 payload 前后需要追加的空白字节数. 上层据此预留空间以便下层原地写头/尾.
	GetFrameBufPreservedSize() FrameBufPreservedSize_t
	// 借用语意: fb 仅在本次调用内借给实现, 实现可读取/原地修改, 但返回后所有权归还调用者,
	// 不得继续持有 fb 引用 (要留数据自行 copy). 调用者可在返回后立即复用同一个 fb 写下一帧.
	WriteFrame(fb *FrameBuf) error
	// 调用者预分配 fb 并管理生命周期. 实现负责把数据帧填进 fb.
	// 成功后 fb.Buf[fb.StartPos:] 是 payload. fb.Buf cap 不够时实现内部 grow (alloc 由调用者 fb 承担).
	// 单 goroutine 调用.
	ReadFrame(fb *FrameBuf) error
	Close() error
}

// 帧缓冲. 调用者持有所有权并负责复用 (借用语意, 见 Frame16Conn_i.WriteFrame/ReadFrame).
// 有效数据为 Buf[StartPos:] . 稳态 0 alloc 由调用者复用同一个 fb (配合 Reset/ResetWithReserve) 实现.
type FrameBuf struct {
	Buf      []byte
	StartPos uint16
}

// 把 fb 准备成可写 needCap 字节的状态以便复用: cap 够则 reslice, 不够才重新分配. StartPos 归零.
func (fb *FrameBuf) Reset(needCap int) {
	if cap(fb.Buf) < needCap {
		fb.Buf = make([]byte, needCap)
	} else {
		fb.Buf = fb.Buf[:needCap]
	}
	fb.StartPos = 0
}

// 按某层的 prefix/suffix preserved size 把 fb 准备成带预留空间的状态以便复用 (复用底层数组).
// 约定保持不变: payload 区 = fb.Buf[fb.StartPos:] (即整个 tail 都是 payload, 长度为 payloadLen).
// suffix 区藏在 cap 里: cap(fb.Buf) >= len(fb.Buf) + suffix, 下层实现可通过
// fb.Buf = fb.Buf[:len(fb.Buf)+selfSuffix] 扩展 len 到 suffix 区, 用于原地填写 footer.
// fb.StartPos = prefix, 下层实现可通过 fb.StartPos -= selfPrefix 占用 prefix 区, 用于原地填写 header.
func (fb *FrameBuf) ResetWithReserve(prefix int, payloadLen int, suffix int) {
	fb.Reset(prefix + payloadLen + suffix)
	fb.Buf = fb.Buf[:prefix+payloadLen]
	fb.StartPos = uint16(prefix)
}

// 分配一个新的 FrameBuf, 可写 needCap 字节. 用于一次性/冷路径; 热路径应复用 fb + Reset.
func GetFrameBuf(needCap int) *FrameBuf {
	return &FrameBuf{Buf: make([]byte, needCap)}
}

// 分配一个新的带 prefix/suffix 预留空间的 FrameBuf. 语意同 ResetWithReserve.
// 用于一次性/冷路径; 热路径应复用 fb + ResetWithReserve.
func GetFrameBufWithReserve(prefix int, payloadLen int, suffix int) *FrameBuf {
	fb := &FrameBuf{}
	fb.ResetWithReserve(prefix, payloadLen, suffix)
	return fb
}
