package zlibChannel

// 可增长的环形缓冲区, 支持零拷贝分段发送.
// 使用场景: tcp连接上写一个frame协议,一边异步不阻塞写入(满了之后断开或者阻塞), 一边写入线程只管写入.
// 初始128字节, 每次2倍增长, 直到MaxCap. 只扩容不缩容.
// 扩容时复制未发送的队列数据到新buffer, 其他时候不移动数据.
// 不含锁, 调用者负责同步.
//
// 使用示例见 Frame16BipBuf_example_test.go:
//   - TestExample_TcpWriteBuffer: tcp协议写buffer, 用 TakeSendSlice 取最大连续缓冲区发送.
//   - TestExample_StreamReadBuffer: tcp多路复用stream读buffer, 用 PopReadFrame 逐帧取出处理.
//
// 内部布局与 Frame16BipFixBuf 相同:
//
//	非回绕: [free:0..Sent][S:Sent..SendEnd][Q:SendEnd..Write][free:Write..N]
//	回绕:   [Q_head:0..Write][free:Write..Sent][S:Sent..SendEnd][Q_tail:SendEnd..TailEnd][unused:TailEnd..N]
//
// 设计限制: 最大写入数据长度 65535 (64KB-1)
// 实现约束:
// * TakeSendSlice 不移动数据,但是可能多次发送才能清空已发送数据.
// * 每次发送使用最大的连续buffer,并且最大化利用已有buffer,
// * 先写入先发送.(fifo)
// * 每次写入的数据不能截断.
// * 正在发送的buffer不能写入.
// * 在尝试最大限制体积前, AllocFrame 不会报错.
// * TakeSendSlice 取走的缓存区,在下次GetNextSendBuffer调用前总是有效
// * 在内部不移动数据的前提下,尽量延缓 扩容这个动作.
// * 只会扩容,不会缩容.
// * 扩容可以 移动/复制 数据.
type Frame16BipBuf struct {
	Buf     []byte
	Sent    uint32
	SendEnd uint32
	Write   uint32
	TailEnd uint32
	MaxCap  uint32
	OldBuf  []byte // 持有扩容前的buffer引用, 保证TakeSendSlice返回的slice在下次调用前有效
}

// 创建可增长BipBuffer. maxCap为最大容量(字节).
func NewFrame16BipBuf(maxCap uint32) Frame16BipBuf {
	return Frame16BipBuf{
		MaxCap: maxCap,
	}
}

// 获取n字节的写入缓冲区, 自动写入uint16小端长度头.
// 空间不足时自动扩容(一次性算出目标体积). 达到MaxCap后仍不足返回nil.
// 返回的切片长度为n, 调用者填入数据即可.
func (b *Frame16BipBuf) AllocFrame(n uint16) []byte {
	if n == 0 {
		return nil
	}
	total := 2 + uint32(n)

	cap_ := uint32(len(b.Buf))

	if b.TailEnd == 0 {

		if b.Write+total <= cap_ {
			pos := b.Write
			b.Buf[pos] = byte(n)
			b.Buf[pos+1] = byte(n >> 8)
			b.Write += total
			return b.Buf[pos+2 : pos+total]
		}
		if total <= b.Sent {
			b.TailEnd = b.Write
			b.Buf[0] = byte(n)
			b.Buf[1] = byte(n >> 8)
			b.Write = total
			return b.Buf[2:total]
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

// 拿走下一个用于发送的最大连续缓冲区.
// 隐式确认上一次拿走已完成(Sent推进到SendEnd).
// 返回的切片保证在下一次 TakeSendSlice/PopReadFrame 调用前有效.
// 返回nil表示没有待发送数据.
// 内部是多个完整的frame 类似 [uint16 frameLen][frame bytes]
// 可与 PopReadFrame 混用: 两者共享 Sent 游标, 隐式确认机制统一.
func (b *Frame16BipBuf) TakeSendSlice() []byte {
	b.OldBuf = b.Buf
	if b.Sent < b.SendEnd {
		b.Sent = b.SendEnd
	}
	if b.TailEnd > 0 && b.Sent >= b.TailEnd {
		b.Sent = 0
		b.SendEnd = 0
		b.TailEnd = 0
	}
	if b.TailEnd > 0 {
		b.SendEnd = b.TailEnd
		return b.Buf[b.Sent:b.SendEnd]
	}
	if b.Sent < b.Write {
		b.SendEnd = b.Write
		return b.Buf[b.Sent:b.SendEnd]
	}
	b.Sent = 0
	b.SendEnd = 0
	b.Write = 0

	return nil
}

// 逐帧拿走下一个已写入的frame数据(不含2字节长度头).
// 与 TakeSendSlice 共享 Sent 游标: 隐式确认上次拿走已完成(Sent推进到SendEnd), 然后弹出一帧.
// 没有更多数据时返回nil.
// 可与 TakeSendSlice 混用: 两者都是"拿走"语义, 隐式确认机制统一.
func (b *Frame16BipBuf) PopReadFrame() []byte {
	b.OldBuf = b.Buf

	if b.Sent < b.SendEnd {
		b.Sent = b.SendEnd
	}

	if b.TailEnd > 0 && b.Sent >= b.TailEnd {
		b.Sent = 0
		b.SendEnd = 0
		b.TailEnd = 0
	}

	cur := b.Sent

	if b.TailEnd == 0 {

		if cur+2 > b.Write {

			if cur >= b.Write {
				b.Sent = 0
				b.SendEnd = 0
				b.Write = 0
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

	b.Sent = 0
	b.SendEnd = 0
	b.TailEnd = 0
	cur = 0

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

// 清空缓冲区(不释放内存, 不缩容).
func (b *Frame16BipBuf) Reset() {
	b.Sent = 0
	b.SendEnd = 0
	b.Write = 0
	b.TailEnd = 0
	b.OldBuf = nil
}

// 扩容buffer使其能容纳当前队列数据加上needTotal字节的新写入.
// 从当前大小开始2倍增长直到满足需求或达到MaxCap.
// 返回false表示已达最大容量无法扩容.
func (b *Frame16BipBuf) grow(needTotal uint32) bool {
	curSize := uint32(len(b.Buf))
	if curSize >= b.MaxCap {
		return false
	}

	// 计算当前队列数据长度
	var queueLen uint32
	if b.TailEnd == 0 {
		queueLen = b.Write - b.SendEnd
	} else {
		queueLen = (b.TailEnd - b.SendEnd) + b.Write
	}
	if curSize == 0 {
		curSize = 64
	}

	required := queueLen + needTotal
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

	if b.TailEnd == 0 {
		copy(newBuf, b.Buf[b.SendEnd:b.Write])
	} else {
		tailLen := b.TailEnd - b.SendEnd
		copy(newBuf, b.Buf[b.SendEnd:b.TailEnd])
		copy(newBuf[tailLen:], b.Buf[:b.Write])
	}

	b.Buf = newBuf
	b.Sent = 0
	b.SendEnd = 0
	b.Write = queueLen
	b.TailEnd = 0
	return true
}

