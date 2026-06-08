package zlibChannel

// Frame16BipBuf2 是 Frame16BipBuf 的带前置留空(Prefix)版本.
//
// 加 Prefix 的原因:
//   上层(websocket 写入)在把一批待发送数据交给底层 socket 前, 需要在数据最前面拼一个
//   websocket 帧头(2~14 字节). 老的 Frame16BipBuf.TakeSendSlice 返回的切片紧贴数据起点,
//   前面没有空闲字节, 于是 websocket 层只能把整批 payload 再 copy 到另一个带头部空间的
//   buffer 里(WriteMsg 里的 copy(writeBuf[pos:], buf)). 这是稳定状态写入路径上唯一可消除的
//   整 payload copy.
//   Frame16BipBuf2 在每个发送批次(TakeSendSlice 返回的连续块)的数据起点前, 始终保留 Prefix
//   个可写字节, 这样 websocket 层可以把帧头"右对齐"原地写进这段预留区, 然后单次 Write 发出
//   [帧头 + payload], 完全不 copy payload. 即 Frame16Conn_i.WriteFrame 的"前置留空"约定.
//
// 实现方式: 把数据区整体上移 Prefix 字节, 即数据只占用 Buf[Prefix:], 而 Buf[0:Prefix] 作为
// 第一个批次/回绕后批次的永久帧头 scratch. 对于流中间的批次, 其数据起点前面正好是"上一个已发送
// 批次的尾部"(已经发出, 可安全覆盖), 天然就是可用 scratch, 无需额外预留.
// 因此只要保证"任何批次的数据起点 Sent 都 >= Prefix"即可, 做法是把所有线性起点 0 改为 Prefix.
//
// TakeSendSlice 返回 Buf[Sent-Prefix : SendEnd]: 前 Prefix 字节是帧头 scratch, 真正 payload
// 是返回切片的 [Prefix:]. 配合 zlibVnet.FrameBuf{Buf: 返回值, StartPos: Prefix} 使用.
// Prefix==0 时行为与 Frame16BipBuf 完全一致(向后兼容).
//
// 其余环形缓冲语意与 Frame16BipBuf 相同, 见该文件注释.
type Frame16BipBuf2 struct {
	Buf     []byte
	Sent    uint32
	SendEnd uint32
	Write   uint32
	TailEnd uint32
	MaxCap  uint32
	Prefix  uint32 // 每个发送批次数据起点前预留的帧头 scratch 字节数.
	OldBuf  []byte // 持有扩容前的buffer引用, 保证TakeSendSlice返回的slice在下次调用前有效
}

// 创建带前置留空的可增长BipBuffer. maxCap为最大容量(字节), prefix为每批发送预留的帧头空间.
// 初始把游标置于 prefix, 保证第一个写入的 frame 数据起点 >= prefix.
func NewFrame16BipBuf2(maxCap uint32, prefix uint16) Frame16BipBuf2 {
	return Frame16BipBuf2{
		MaxCap: maxCap,
		Prefix: uint32(prefix),
		Sent:   uint32(prefix),
		SendEnd: uint32(prefix),
		Write:  uint32(prefix),
	}
}

// 获取n字节的写入缓冲区, 自动写入uint16小端长度头.
// 空间不足时自动扩容(一次性算出目标体积). 达到MaxCap后仍不足返回nil.
// 返回的切片长度为n, 调用者填入数据即可.
func (b *Frame16BipBuf2) AllocFrame(n uint16) []byte {
	if n == 0 {
		return nil
	}
	total := 2 + uint32(n)

	cap_ := uint32(len(b.Buf))

	if b.TailEnd == 0 {
		if cap_ > 0 && b.Write+total <= cap_ {
			pos := b.Write
			b.Buf[pos] = byte(n)
			b.Buf[pos+1] = byte(n >> 8)
			b.Write += total
			return b.Buf[pos+2 : pos+total]
		}
		// 回绕到头部: 底部可写区是 [Prefix, Sent), 同样要给回绕后的首批次留出 Prefix scratch,
		// 所以从 Prefix 处开始写, 需要 Prefix+total <= Sent.
		if cap_ > 0 && b.Prefix+total <= b.Sent {
			b.TailEnd = b.Write
			pos := b.Prefix
			b.Buf[pos] = byte(n)
			b.Buf[pos+1] = byte(n >> 8)
			b.Write = pos + total
			return b.Buf[pos+2 : b.Write]
		}
	} else {
		if b.Write+total <= b.Sent {
			pos := b.Write
			b.Buf[pos] = byte(n)
			b.Buf[pos+1] = byte(n >> 8)
			b.Write += total
			return b.Buf[pos+2 : pos+total]
		}
	}

	if !b.grow(total) {
		return nil
	}

	pos := b.Write
	b.Buf[pos] = byte(n)
	b.Buf[pos+1] = byte(n >> 8)
	b.Write += total
	return b.Buf[pos+2 : pos+total]
}

// 拿走下一个用于发送的最大连续缓冲区, 前面含 Prefix 字节帧头 scratch.
// 返回切片布局: [Prefix 字节 scratch][数据(多个完整 [uint16 len][frame] )]. 真正 payload 是返回值的 [Prefix:].
// 隐式确认上一次拿走已完成(Sent推进到SendEnd). 返回nil表示没有待发送数据.
func (b *Frame16BipBuf2) TakeSendSlice() []byte {
	b.OldBuf = b.Buf
	if b.Sent < b.SendEnd {
		b.Sent = b.SendEnd
	}
	if b.TailEnd > 0 && b.Sent >= b.TailEnd {
		// 尾段(上半区)发送完, 切换到回绕后的头段(下半区), 数据从 Prefix 处开始.
		b.Sent = b.Prefix
		b.SendEnd = b.Prefix
		b.TailEnd = 0
	}
	if b.TailEnd > 0 {
		b.SendEnd = b.TailEnd
		return b.Buf[b.Sent-b.Prefix : b.SendEnd]
	}
	if b.Sent < b.Write {
		b.SendEnd = b.Write
		return b.Buf[b.Sent-b.Prefix : b.SendEnd]
	}
	// 排空, 复位到 Prefix 基点.
	b.Sent = b.Prefix
	b.SendEnd = b.Prefix
	b.Write = b.Prefix
	return nil
}

// 逐帧拿走下一个已写入的frame数据(不含2字节长度头, 不含 Prefix scratch).
// 与 TakeSendSlice 共享 Sent 游标. 没有更多数据时返回nil.
func (b *Frame16BipBuf2) PopReadFrame() []byte {
	b.OldBuf = b.Buf

	if b.Sent < b.SendEnd {
		b.Sent = b.SendEnd
	}

	if b.TailEnd > 0 && b.Sent >= b.TailEnd {
		b.Sent = b.Prefix
		b.SendEnd = b.Prefix
		b.TailEnd = 0
	}

	cur := b.Sent

	if b.TailEnd == 0 {
		if cur+2 > b.Write {
			if cur >= b.Write {
				b.Sent = b.Prefix
				b.SendEnd = b.Prefix
				b.Write = b.Prefix
			}
			return nil
		}
		n := uint32(b.Buf[cur]) | uint32(b.Buf[cur+1])<<8
		end := cur + 2 + n
		if end > b.Write {
			return nil
		}
		b.Sent = end
		b.SendEnd = end
		return b.Buf[cur+2 : end]
	}

	if cur+2 <= b.TailEnd {
		n := uint32(b.Buf[cur]) | uint32(b.Buf[cur+1])<<8
		end := cur + 2 + n
		if end <= b.TailEnd {
			b.Sent = end
			b.SendEnd = end
			return b.Buf[cur+2 : end]
		}
	}

	b.Sent = b.Prefix
	b.SendEnd = b.Prefix
	b.TailEnd = 0
	cur = b.Prefix

	if cur+2 > b.Write {
		return nil
	}
	n := uint32(b.Buf[cur]) | uint32(b.Buf[cur+1])<<8
	end := cur + 2 + n
	if end > b.Write {
		return nil
	}
	b.Sent = end
	b.SendEnd = end
	return b.Buf[cur+2 : end]
}

// 清空缓冲区(不释放内存, 不缩容). 复位到 Prefix 基点.
func (b *Frame16BipBuf2) Reset() {
	b.Sent = b.Prefix
	b.SendEnd = b.Prefix
	b.Write = b.Prefix
	b.TailEnd = 0
	b.OldBuf = nil
}

// 扩容buffer使其能容纳当前队列数据加上needTotal字节的新写入(物理上还要额外容纳 Prefix 基点偏移).
// 从当前大小开始2倍增长直到满足需求或达到MaxCap. 返回false表示已达最大容量无法扩容.
// 扩容后数据线性化到 newBuf[Prefix:], 游标复位到 Prefix 基点.
func (b *Frame16BipBuf2) grow(needTotal uint32) bool {
	curSize := uint32(len(b.Buf))
	if curSize >= b.MaxCap {
		return false
	}

	// 计算当前队列数据长度
	var queueLen uint32
	if b.TailEnd == 0 {
		queueLen = b.Write - b.SendEnd
	} else {
		queueLen = (b.TailEnd - b.SendEnd) + (b.Write - b.Prefix)
	}
	if curSize == 0 {
		curSize = 64
	}

	required := b.Prefix + queueLen + needTotal
	newSize := curSize * 2
	for newSize < required && newSize < b.MaxCap {
		if newSize > b.MaxCap/2 {
			newSize = b.MaxCap
		} else {
			newSize *= 2
		}
	}
	if newSize > b.MaxCap {
		newSize = b.MaxCap
	}
	if newSize < required {
		return false
	}

	newBuf := make([]byte, newSize)

	// 只复制队列数据(未发送部分)到 newBuf[Prefix:], 不复制正在发送的 [Sent..SendEnd).
	// 正在发送的 slice 由调用者持有, 仍指向旧底层数组(Go slice 语义保证有效), OldBuf 阻止其被 GC.
	// len(b.Buf)==0 是首次分配, 此时 queueLen==0, 跳过 copy(避免对 nil buf 越界切片).
	if len(b.Buf) > 0 {
		if b.TailEnd == 0 {
			copy(newBuf[b.Prefix:], b.Buf[b.SendEnd:b.Write])
		} else {
			tailLen := b.TailEnd - b.SendEnd
			copy(newBuf[b.Prefix:], b.Buf[b.SendEnd:b.TailEnd])
			copy(newBuf[b.Prefix+tailLen:], b.Buf[b.Prefix:b.Write])
		}
	}

	b.Buf = newBuf
	b.Sent = b.Prefix
	b.SendEnd = b.Prefix
	b.Write = b.Prefix + queueLen
	b.TailEnd = 0
	return true
}
