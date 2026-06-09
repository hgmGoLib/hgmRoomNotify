package zlibChannel

// Frame16BipBuf2 是 Frame16BipBuf 的带前置/后置留空(Prefix/Suffix)+ 单帧封顶版本.
//
// 前置留空(Prefix)的原因:
//   下层(如 websocket 写入)在把一批待发送数据交给 socket 前, 需要在数据最前面拼一个帧头.
//   TakeSendSlice 在每个发送批次的数据起点前始终保留 Prefix 个可写字节, 下层把帧头"右对齐"
//   原地写进这段预留区, 单次 Write 发出 [帧头 + payload], 完全不 copy payload.
//   实现: 数据只占用 Buf[Prefix:], Buf[0:Prefix] 是首批次/回绕后批次的帧头 scratch; 流中间批次
//   的数据起点前正好是"上一个已发送批次的尾部"(已发出, 可安全覆盖), 天然就是 scratch.
//
// 后置留空(Suffix)的原因:
//   有些下层(如 AES-GCM record = [2B len][密文 + 16B tag])需要在 payload 之后原地写帧尾.
//   与 Prefix 不同, payload 之后正是"未发送数据区", 不能直接借用. 因此只在一个发送批次"排空"
//   (取走时其后没有已写入的待发帧)时才能授予 Suffix scratch: 从环形 buffer 尾部空闲区借出
//   Suffix 字节, 并把写游标推过这段 gap, 防止后续 AllocFrame 写进正在发送的 tag scratch.
//   返回切片用三索引切片限制 cap, 使下层据 cap-len 判断本批是否拿到了 Suffix 空白:
//   拿到则原地写 tag (0 copy), 没拿到(批次未排空)则下层走 copy 回退路径, 仍正确.
//   Suffix==0 时整个后置留空逻辑关闭, 行为与只带 Prefix 完全一致.
//
// 单帧封顶(MaxFramePayload)的原因:
//   下层最终写入(含 Prefix/Suffix)硬限 Frame16MaxWriteSize. MaxFramePayload = 该硬限减去
//   Prefix/Suffix, 即单个发送批次 payload 的上限. TakeSendSlice 按子帧边界(不切断任何
//   [uint16 len][data])累加到不超过 MaxFramePayload 为止, 余下数据留给下一次取走.
//   MaxFramePayload==0 表示不封顶(取走当前全部连续数据).
//
// 其余环形缓冲语意与 Frame16BipBuf 相同, 见该文件注释.
type Frame16BipBuf2 struct {
	Buf             []byte
	Sent            uint32
	SendEnd         uint32
	Write           uint32
	TailEnd         uint32
	MaxCap          uint32
	Prefix          uint32 // 每个发送批次数据起点前预留的帧头 scratch 字节数.
	Suffix          uint32 // 排空批次时在 payload 之后授予的帧尾 scratch 字节数.
	MaxFramePayload uint32 // 单个发送批次 payload 上限(含各子帧 2 字节长度头), 0 表示不封顶.
	pendingGap      uint32 // 上次取走已授予 Suffix 时, payload 末尾到下一批数据起点之间预留的 gap.
	OldBuf          []byte // 持有扩容前的buffer引用, 保证TakeSendSlice返回的slice在下次调用前有效
}

// 创建带前置/后置留空 + 单帧封顶的可增长BipBuffer.
// maxCap 为最大容量(字节); prefix/suffix 为每批发送预留的帧头/帧尾空间; maxFramePayload 为单批
// payload 上限(0 不封顶). 初始把游标置于 prefix, 保证第一个写入的 frame 数据起点 >= prefix.
func NewFrame16BipBuf2(maxCap uint32, prefix uint16, suffix uint16, maxFramePayload uint32) Frame16BipBuf2 {
	return Frame16BipBuf2{
		MaxCap:          maxCap,
		Prefix:          uint32(prefix),
		Suffix:          uint32(suffix),
		MaxFramePayload: maxFramePayload,
		Sent:            uint32(prefix),
		SendEnd:         uint32(prefix),
		Write:           uint32(prefix),
	}
}

// 获取n字节的写入缓冲区, 自动写入uint16小端长度头.
// 空间不足时自动扩容(一次性算出目标体积). 达到MaxCap后仍不足返回nil.
// 单子帧体积(2+n)超过 MaxFramePayload 时返回 nil(否则该帧永远无法被 TakeSendSlice 取出).
// 返回的切片长度为n, 调用者填入数据即可.
func (b *Frame16BipBuf2) AllocFrame(n uint16) []byte {
	if n == 0 {
		return nil
	}
	total := 2 + uint32(n)
	if b.MaxFramePayload > 0 && total > b.MaxFramePayload {
		return nil
	}

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

// 从 start 处按子帧边界([uint16 len][data]) 向前累加, 返回不超过 MaxFramePayload 的最后一个
// 完整子帧边界(返回值 <= limit). MaxFramePayload==0 时直接返回 limit. AllocFrame 保证每个子帧
// 2+n <= MaxFramePayload, 故至少累加一个子帧, 返回值必定 > start.
func (b *Frame16BipBuf2) frameAlignedEnd(start, limit uint32) uint32 {
	if b.MaxFramePayload == 0 {
		return limit
	}
	end := start
	for end < limit {
		n := uint32(b.Buf[end]) | uint32(b.Buf[end+1])<<8
		total := 2 + n
		if end+total-start > b.MaxFramePayload {
			break
		}
		end += total
	}
	return end
}

// 拿走下一个用于发送的连续缓冲区, 前面含 Prefix 字节帧头 scratch.
// 返回切片布局: [Prefix scratch][payload(若干完整 [uint16 len][data], 总长 <= MaxFramePayload)].
// 真正 payload 是返回值的 [Prefix:]. 当本批"排空"且 Suffix>0 时, 返回切片的 cap 比 len 多出 Suffix
// 字节(三索引切片), 供下层原地写帧尾; 否则 cap==len, 下层据此走 copy 回退.
// 隐式确认上一次拿走已完成(Sent推进到SendEnd, 并跳过上次授予的 Suffix gap). 返回nil表示没有待发送数据.
func (b *Frame16BipBuf2) TakeSendSlice() []byte {
	b.OldBuf = b.Buf
	if b.Sent < b.SendEnd {
		b.Sent = b.SendEnd
	}
	// 跳过上一批授予的 Suffix gap: 下一批 payload 起点在 gap 之后.
	b.Sent += b.pendingGap
	b.pendingGap = 0

	if b.TailEnd > 0 && b.Sent >= b.TailEnd {
		// 尾段(上半区)发送完, 切换到回绕后的头段(下半区), 数据从 Prefix 处开始.
		b.Sent = b.Prefix
		b.SendEnd = b.Prefix
		b.TailEnd = 0
	}
	if b.TailEnd > 0 {
		// 回绕未发完的尾段: 不授予 Suffix(其后是已写入的头段待发数据).
		end := b.frameAlignedEnd(b.Sent, b.TailEnd)
		b.SendEnd = end
		return b.Buf[b.Sent-b.Prefix : end]
	}
	if b.Sent < b.Write {
		end := b.frameAlignedEnd(b.Sent, b.Write)
		b.SendEnd = end
		// 仅当本批排空(end==Write, 其后没有待发数据)且尾部有连续空闲时, 才授予 Suffix scratch.
		if b.Suffix > 0 && end == b.Write && uint32(len(b.Buf))-b.Write >= b.Suffix {
			b.Write += b.Suffix    // 推过 gap, 防后续 AllocFrame 写进正在发送的 tag scratch
			b.pendingGap = b.Suffix // 下次取走时确认并跳过这段 gap
			return b.Buf[b.Sent-b.Prefix : end : end+b.Suffix]
		}
		return b.Buf[b.Sent-b.Prefix : end]
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
	b.Sent += b.pendingGap
	b.pendingGap = 0

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

// 返回在不超过 MaxCap 的前提下, 还能用 AllocFrame 写入的总字节数(含每帧 2 字节长度头).
// 调用者据此判断一组 frame 能否一次性原子写入: 当 sum(2+n_i) <= FreeForAlloc() 时,
// 在同一把锁内(期间不发送)连续 AllocFrame 这组 frame 必定全部成功(grow 最多扩容到 MaxCap,
// 且因不发送不会回绕, 始终连续). 据此实现"全部写入或全部不写入"的原子分块.
func (b *Frame16BipBuf2) FreeForAlloc() uint32 {
	var queueLen uint32
	if b.TailEnd == 0 {
		queueLen = b.Write - b.SendEnd
	} else {
		queueLen = (b.TailEnd - b.SendEnd) + (b.Write - b.Prefix)
	}
	used := b.Prefix + queueLen
	if used >= b.MaxCap {
		return 0
	}
	return b.MaxCap - used
}

// 清空缓冲区(不释放内存, 不缩容). 复位到 Prefix 基点.
func (b *Frame16BipBuf2) Reset() {
	b.Sent = b.Prefix
	b.SendEnd = b.Prefix
	b.Write = b.Prefix
	b.TailEnd = 0
	b.pendingGap = 0
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

	// 只复制队列数据(未发送部分, 含已授予但未确认的 Suffix gap)到 newBuf[Prefix:],
	// 不复制正在发送的 [Sent..SendEnd). 正在发送的 slice 由调用者持有, 仍指向旧底层数组
	// (Go slice 语义保证有效), OldBuf 阻止其被 GC. len(b.Buf)==0 是首次分配, queueLen==0, 跳过 copy.
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
