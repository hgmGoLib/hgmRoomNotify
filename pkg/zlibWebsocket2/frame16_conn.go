package zlibWebsocket2

import (
	"encoding/binary"
	"errors"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibVnet"
)

// *Conn_t 满足 zlibVnet.Frame16Conn_i: 一个 frame 对应一个 websocket 二进制消息.
// frame 内部字节由上层自行打包(本库上层用 [uint16LE len][msg]... 子帧), 本层不解释.
var _ zlibVnet.Frame16Conn_i = (*Conn_t)(nil)

// 下层在 payload 前后需要追加的空白字节数.
// Prefix = websocket 帧头最大长度: 最终写入硬限 Frame16MaxWriteSize = 64KB(含帧头), 故 payload 必然 < 65536,
// payload 永远用不到 8 字节扩展长度, 帧头最多 2 + 2(16 位扩展长度) = 4 字节; 掩码(客户端)再 +4 = 8.
// 调用者在 payload 前预留这么多字节, WriteFrame 就能把帧头原地右对齐写进预留区,
// 单次 Write 发出 [帧头+payload] 而不必把 payload 再 copy 到带头部空间的 buffer.
// Suffix = 0: websocket 自带分帧, 不需要调用者预留 footer 空间.
func (conn *Conn_t) GetFrameBufPreservedSize() zlibVnet.FrameBufPreservedSize_t {
	if conn.isWriteMask {
		return zlibVnet.FrameBufPreservedSize_t{Prefix: 8, Suffix: 0}
	}
	return zlibVnet.FrameBufPreservedSize_t{Prefix: 4, Suffix: 0}
}

// 把 fb.Buf[fb.StartPos:] 作为一个 websocket 二进制消息发送. 借用语意: 返回后不持有 fb.
// 若 fb.StartPos 处前面留有足够的帧头空间(>= 实际帧头长度), 则原地写帧头、单次发送, 零 payload copy;
// 否则退回 WriteMsg 的 copy 路径(把 payload 拷进 writeCache 再拼头).
func (conn *Conn_t) WriteFrame(fb *zlibVnet.FrameBuf) error {
	payload := fb.Buf[fb.StartPos:]
	hdrLen := wsHeaderLen(len(payload), conn.isWriteMask)
	if int(fb.StartPos) >= hdrLen {
		errMsg := conn.writeFrameInPlace(fb.Buf, int(fb.StartPos), len(payload), hdrLen)
		if errMsg != "" {
			return errors.New(errMsg)
		}
		return nil
	}
	errMsg := conn.WriteMsg(payload)
	if errMsg != "" {
		return errors.New(errMsg)
	}
	return nil
}

// websocket 帧头长度(字节). dataLen 为 payload 长度, isMask 表示是否带 4 字节 mask key.
func wsHeaderLen(dataLen int, isMask bool) int {
	n := 2
	switch {
	case dataLen >= 65536:
		n += 8
	case dataLen > 125:
		n += 2
	}
	if isMask {
		n += 4
	}
	return n
}

// 原地右对齐写帧头: 帧头占 buf[startPos-hdrLen : startPos], 紧接其后就是 payload buf[startPos:startPos+dataLen],
// 两段连续, 单次 Write 发出. 掩码连接(客户端)在写头时顺带原地 XOR payload(借用语意允许原地修改 fb).
func (conn *Conn_t) writeFrameInPlace(buf []byte, startPos, dataLen, hdrLen int) (errMsg string) {
	hdr := buf[startPos-hdrLen : startPos]
	hdr[0] = 0x82 // FIN + binary opcode
	b1 := byte(0)
	if conn.isWriteMask {
		b1 = 0x80
	}
	pos := 0
	switch {
	case dataLen >= 65536:
		hdr[1] = b1 | 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(dataLen))
		pos = 10
	case dataLen > 125:
		hdr[1] = b1 | 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(dataLen))
		pos = 4
	default:
		hdr[1] = b1 | byte(dataLen)
		pos = 2
	}
	if conn.isWriteMask {
		maskBuf := hdr[pos : pos+4]
		errMsg = randDataRead(maskBuf)
		if errMsg != "" {
			return errMsg
		}
		data := buf[startPos : startPos+dataLen]
		for i := range data {
			data[i] ^= maskBuf[i&3]
		}
	}
	_, err := conn.writer.Write(buf[startPos-hdrLen : startPos+dataLen])
	if err != nil {
		return err.Error()
	}
	return ""
}

// 读取一个 websocket 消息填入 fb. 成功后 fb.Buf[fb.StartPos:] 为 payload (StartPos=0).
// 读帧上限固定 Frame16MaxWriteSize(下层不能控制), 超限拒收.
func (conn *Conn_t) ReadFrame(fb *zlibVnet.FrameBuf) error {
	conn.readCache.Reset()
	errMsg := conn.ReadMsg(&conn.readCache, zlibVnet.Frame16MaxWriteSize)
	if errMsg != "" {
		return errors.New(errMsg)
	}
	data := conn.readCache.GetBytes()
	fb.Reset(len(data))
	copy(fb.Buf, data)
	fb.StartPos = 0
	return nil
}
