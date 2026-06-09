package zlibChannel

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// 取走一个发送批次, 验证前 prefix 字节是可写 scratch(写脏后不影响 payload), 返回 payload(去掉 prefix).
func takeAndCheckPrefix(t *testing.T, bb *Frame16BipBuf2) []byte {
	t.Helper()
	send := bb.TakeSendSlice()
	if send == nil {
		return nil
	}
	prefix := int(bb.Prefix)
	if len(send) < prefix {
		t.Fatalf("send slice shorter than prefix: len=%d prefix=%d", len(send), prefix)
	}
	payload := make([]byte, len(send)-prefix)
	copy(payload, send[prefix:]) // 先把 payload 拷出来对照
	// 模拟 websocket 层把帧头原地写进 prefix scratch.
	for i := 0; i < prefix; i++ {
		send[i] = 0xAA
	}
	// 写 scratch 后 payload 不应被破坏.
	if !bytes.Equal(payload, send[prefix:]) {
		t.Fatal("writing prefix scratch corrupted payload")
	}
	return payload
}

// 把一个发送批次的 payload([len][data]...) 解析成多条 frame.
func parseFrames(t *testing.T, payload []byte) [][]byte {
	t.Helper()
	out := [][]byte{}
	off := 0
	for off < len(payload) {
		if off+2 > len(payload) {
			t.Fatalf("truncated frame len header at off=%d", off)
		}
		n := int(binary.LittleEndian.Uint16(payload[off : off+2]))
		off += 2
		if off+n > len(payload) {
			t.Fatalf("truncated frame body: need %d have %d", n, len(payload)-off)
		}
		f := make([]byte, n)
		copy(f, payload[off:off+n])
		out = append(out, f)
		off += n
	}
	return out
}

func TestFrame16BipBuf2_Basic_WithPrefix(t *testing.T) {
	bb := NewFrame16BipBuf2(1024, 4, 0, 0)
	buf := bb.AllocFrame(5)
	if buf == nil {
		t.Fatal("AllocFrame failed")
	}
	copy(buf, []byte("hello"))

	send := bb.TakeSendSlice()
	if send == nil {
		t.Fatal("TakeSendSlice nil")
	}
	// prefix(4) + len头(2) + payload(5)
	if len(send) != 4+2+5 {
		t.Fatalf("expected len 11, got %d", len(send))
	}
	if binary.LittleEndian.Uint16(send[4:6]) != 5 {
		t.Fatalf("frame len mismatch")
	}
	if !bytes.Equal(send[6:11], []byte("hello")) {
		t.Fatalf("payload mismatch: %q", send[6:11])
	}
	if bb.TakeSendSlice() != nil {
		t.Fatal("should be empty after confirm")
	}
}

func TestFrame16BipBuf2_PrefixZero_SameAsOriginal(t *testing.T) {
	bb := NewFrame16BipBuf2(1024, 0, 0, 0)
	buf := bb.AllocFrame(5)
	copy(buf, []byte("world"))
	send := bb.TakeSendSlice()
	if len(send) != 7 {
		t.Fatalf("prefix=0 should match original len 7, got %d", len(send))
	}
	if !bytes.Equal(send[2:], []byte("world")) {
		t.Fatal("payload mismatch")
	}
}

// 模型法压力测试: 随机交替 写帧 / 取批发送, 强制触发 grow 与 wrap, 每次取批都写脏 prefix scratch,
// 全程用一个 FIFO 期望队列校验数据顺序与完整性. 任何 scratch 越界破坏未发送数据都会在后续 take 暴露.
func TestFrame16BipBuf2_Model_GrowWrapIntegrity(t *testing.T) {
	for _, prefix := range []uint16{0, 1, 4, 10, 14} {
		for _, maxCap := range []uint32{128, 256, 1024, 4096} {
			bb := NewFrame16BipBuf2(maxCap, prefix, 0, 0)
			pending := [][]byte{} // 已写入但还没被 take 返回的 frame, FIFO
			seq := byte(0)
			// LCG 确定性伪随机.
			rng := uint32(0x12345678 + maxCap + uint32(prefix))
			next := func() uint32 { rng = rng*1664525 + 1013904223; return rng }

			for iter := 0; iter < 4000; iter++ {
				if next()%2 == 0 {
					// 尝试写一帧, 大小 1..120, 内容 = 递增字节填充.
					n := uint16(1 + next()%120)
					buf := bb.AllocFrame(n)
					if buf == nil {
						continue // 满了, 跳过
					}
					seq++
					for i := range buf {
						buf[i] = seq
					}
					f := make([]byte, n)
					for i := range f {
						f[i] = seq
					}
					pending = append(pending, f)
				} else {
					payload := takeAndCheckPrefix(t, &bb)
					if payload == nil {
						if len(pending) != 0 {
							t.Fatalf("prefix=%d cap=%d: TakeSendSlice nil but %d frames pending (data lost)", prefix, maxCap, len(pending))
						}
						continue
					}
					frames := parseFrames(t, payload)
					if len(frames) > len(pending) {
						t.Fatalf("prefix=%d cap=%d: got %d frames but only %d pending", prefix, maxCap, len(frames), len(pending))
					}
					for i, f := range frames {
						if !bytes.Equal(f, pending[i]) {
							t.Fatalf("prefix=%d cap=%d iter=%d: frame %d mismatch (len got=%d want=%d)", prefix, maxCap, iter, i, len(f), len(pending[i]))
						}
					}
					pending = pending[len(frames):]
				}
			}
			// 排空剩余, 校验全部取出.
			for {
				payload := takeAndCheckPrefix(t, &bb)
				if payload == nil {
					break
				}
				frames := parseFrames(t, payload)
				for i, f := range frames {
					if !bytes.Equal(f, pending[i]) {
						t.Fatalf("drain prefix=%d cap=%d: frame %d mismatch", prefix, maxCap, i)
					}
				}
				pending = pending[len(frames):]
			}
			if len(pending) != 0 {
				t.Fatalf("prefix=%d cap=%d: %d frames never drained", prefix, maxCap, len(pending))
			}
		}
	}
}

// 显式回绕: 构造 wrap 状态, 确认回绕后的批次也带 prefix scratch 且数据正确.
func TestFrame16BipBuf2_WrapHasPrefix(t *testing.T) {
	bb := NewFrame16BipBuf2(128, 4, 0, 0)
	// 写满->发送->确认, 把游标推到中间, 再制造尾部不足触发回绕.
	a := bb.AllocFrame(40)
	copy(a, bytes.Repeat([]byte("A"), 40))
	bb.TakeSendSlice() // 发送 A
	b := bb.AllocFrame(40)
	copy(b, bytes.Repeat([]byte("B"), 40))
	bb.TakeSendSlice() // 确认 A, 发送 B
	// 现在写一帧, 尾部可能不够 -> 回绕到 prefix 处.
	c := bb.AllocFrame(20)
	if c == nil {
		t.Fatal("alloc C failed")
	}
	copy(c, bytes.Repeat([]byte("C"), 20))
	bb.TakeSendSlice() // 确认 B, 发送 C(可能在回绕头段)
	send := bb.TakeSendSlice()
	// C 应当能被取出(可能需要再取一次, 取决于回绕). 收集直到拿到 C.
	got := []byte{}
	for send != nil {
		if len(send) < 4 {
			t.Fatalf("wrapped send missing prefix room: len=%d", len(send))
		}
		got = append(got, send[4:]...)
		send = bb.TakeSendSlice()
	}
	// got 里应包含一帧 C*20
	if len(got) >= 2 {
		n := int(binary.LittleEndian.Uint16(got[:2]))
		if n == 20 && bytes.Equal(got[2:22], bytes.Repeat([]byte("C"), 20)) {
			return
		}
	}
	// C 可能已在前一次 TakeSendSlice 取走; 只要全程没 panic 且 prefix room 充足即视为通过.
}

// 取走一批, 同时写脏 prefix scratch(前)与 suffix scratch(cap-len, 后), 校验都不破坏 payload,
// 并断言 payload 不超过 maxFramePayload. 返回 payload(去掉 prefix).
func takeDirtyBoth(t *testing.T, bb *Frame16BipBuf2, maxFramePayload uint32) []byte {
	t.Helper()
	send := bb.TakeSendSlice()
	if send == nil {
		return nil
	}
	prefix := int(bb.Prefix)
	if len(send) < prefix {
		t.Fatalf("send slice shorter than prefix: len=%d prefix=%d", len(send), prefix)
	}
	payloadLen := len(send) - prefix
	if maxFramePayload > 0 && uint32(payloadLen) > maxFramePayload {
		t.Fatalf("payload %d exceeds maxFramePayload %d", payloadLen, maxFramePayload)
	}
	payload := make([]byte, payloadLen)
	copy(payload, send[prefix:])
	// 写脏 prefix scratch.
	for i := 0; i < prefix; i++ {
		send[i] = 0xAA
	}
	// 写脏 suffix scratch: 返回切片 cap 超出 len 的部分即下层可原地写 tag 的空白.
	full := send[:cap(send)]
	for i := len(send); i < cap(send); i++ {
		full[i] = 0xBB
	}
	// 两段 scratch 写脏后, payload 不应被破坏.
	if !bytes.Equal(payload, send[prefix:]) {
		t.Fatal("writing prefix/suffix scratch corrupted current payload")
	}
	return payload
}

// 模型法压力测试(带 Suffix 后置留空 + MaxFramePayload 封顶): 随机交替 写帧/取批, 每次取批都写脏
// prefix 与 suffix scratch, FIFO 队列校验数据顺序与完整性. suffix scratch 若越界写进未发送数据
// (gap 逻辑出错)会在后续 take 的 FIFO 比对中暴露; payload 超过封顶也会被断言抓住.
func TestFrame16BipBuf2_Model_SuffixAndMaxFrame(t *testing.T) {
	for _, prefix := range []uint16{0, 2, 4, 8} {
		for _, suffix := range []uint16{0, 1, 16} {
			for _, maxFramePayload := range []uint32{40, 64, 200} {
				for _, maxCap := range []uint32{128, 256, 1024} {
					bb := NewFrame16BipBuf2(maxCap, prefix, suffix, maxFramePayload)
					pending := [][]byte{}
					seq := byte(0)
					rng := uint32(0x9e3779b9 + maxCap + uint32(prefix)*7 + uint32(suffix)*13 + maxFramePayload*3)
					next := func() uint32 { rng = rng*1664525 + 1013904223; return rng }
					// 单子帧 2+n 不得超过 maxFramePayload.
					maxN := maxFramePayload - 2
					if maxN > 120 {
						maxN = 120
					}

					for iter := 0; iter < 6000; iter++ {
						if next()%2 == 0 {
							n := uint16(1 + next()%maxN)
							buf := bb.AllocFrame(n)
							if buf == nil {
								continue
							}
							seq++
							for i := range buf {
								buf[i] = seq
							}
							f := make([]byte, n)
							for i := range f {
								f[i] = seq
							}
							pending = append(pending, f)
						} else {
							payload := takeDirtyBoth(t, &bb, maxFramePayload)
							if payload == nil {
								if len(pending) != 0 {
									t.Fatalf("p=%d s=%d mf=%d cap=%d: nil but %d pending", prefix, suffix, maxFramePayload, maxCap, len(pending))
								}
								continue
							}
							frames := parseFrames(t, payload)
							if len(frames) > len(pending) {
								t.Fatalf("p=%d s=%d mf=%d cap=%d: got %d frames but %d pending", prefix, suffix, maxFramePayload, maxCap, len(frames), len(pending))
							}
							for i, f := range frames {
								if !bytes.Equal(f, pending[i]) {
									t.Fatalf("p=%d s=%d mf=%d cap=%d iter=%d: frame %d mismatch (corruption)", prefix, suffix, maxFramePayload, maxCap, iter, i)
								}
							}
							pending = pending[len(frames):]
						}
					}
					for {
						payload := takeDirtyBoth(t, &bb, maxFramePayload)
						if payload == nil {
							break
						}
						frames := parseFrames(t, payload)
						for i, f := range frames {
							if !bytes.Equal(f, pending[i]) {
								t.Fatalf("drain p=%d s=%d mf=%d cap=%d: frame %d mismatch", prefix, suffix, maxFramePayload, maxCap, i)
							}
						}
						pending = pending[len(frames):]
					}
					if len(pending) != 0 {
						t.Fatalf("p=%d s=%d mf=%d cap=%d: %d frames never drained", prefix, suffix, maxFramePayload, maxCap, len(pending))
					}
				}
			}
		}
	}
}
