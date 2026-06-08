package zlibWebsocket2

import (
	"crypto/rand"
	"encoding/binary"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibBytes"
	"io"
	"strconv"
)

// 这个对象 的 读取和写入调用者应该 分别单线程调用(两者之间可以并发) 注意写入应该挂锁.
// 关闭之后 当前阻塞的读取和写入 会在较短时间后 退出,并且报错.
type Conn_t struct {
	writer      io.Writer
	reader      io.Reader
	closer      io.Closer
	writeCache  zlibBytes.BufWriter
	isWriteMask bool
	// 单条消息最大读取字节数. 0表示不限制. 调用者构造后设置, 供 ReadFrame/GetMaxFrameSize 使用.
	MaxReadMsgSize uint32
	// ReadFrame 复用的读缓冲.
	readCache zlibBytes.BufWriter
}

// 读取时,限制最大写入体积 MaxReadMsgSize 0 表示 不限制. 数据追加到 bufWriter 后面.记得 调用前 Reset 调用这个 bufWriter.
func (conn *Conn_t) ReadMsg(bufWriter *zlibBytes.BufWriter, MaxReadMsgSize uint32) (errMsg string) {
	for {
		buf1 := bufWriter.GetHeadBuffer(2)
		_, err := io.ReadFull(conn.reader, buf1)
		if err != nil {
			if err == io.EOF {

				return "readMsg0 EOF"
			}
			return "34m36vatwy " + err.Error()
		}
		isFin := (buf1[0] & 0x80) > 0
		if isFin == false {
			return "ta34v6xm4v not support fragmentation"
		}
		opcode := buf1[0] & 0xf
		isMask := (buf1[1] & 0x80) > 0
		frameLen := int(buf1[1] & 0x7f)
		toReadLen := 0
		if frameLen == 126 {
			toReadLen += 2
		} else if frameLen == 127 {
			toReadLen += 8
		}
		if isMask {
			toReadLen += 4
		}
		var maskBuf [4]byte
		if toReadLen > 0 {
			buf1 = bufWriter.GetHeadBuffer(toReadLen)

			_, err = io.ReadFull(conn.reader, buf1[:toReadLen])
			if err != nil {
				return "ReadMsg2 " + err.Error()
			}
			pos := 0
			if frameLen == 126 {
				frameLen = int(binary.BigEndian.Uint16(buf1[:2]))
				pos += 2
			} else if frameLen == 127 {
				frameLen64 := binary.BigEndian.Uint64(buf1[:8])
				if frameLen64 > (1 << 31) {
					return "mm25s3yfpc framelen too large"
				}
				frameLen = int(frameLen64)
				pos += 8
			}
			if isMask {
				copy(maskBuf[:], buf1[pos:])
			}
		}
		if MaxReadMsgSize > 0 && uint64(frameLen) > uint64(MaxReadMsgSize) {
			return `fqenu2sm8n frameLen too large frameLen:[` + strconv.Itoa(frameLen) + `] limit:[` + strconv.FormatUint(uint64(MaxReadMsgSize), 10) + `]`
		}
		buf1 = bufWriter.GetHeadBuffer(frameLen)
		_, err = io.ReadFull(conn.reader, buf1)
		if err != nil {
			return "ReadMsg3 " + err.Error()
		}

		if opcode == 8 {
			if conn.isWriteMask {
				var resp [6]byte
				resp[0] = 0x88
				resp[1] = 0x80
				randDataRead(resp[2:6])
				conn.writer.Write(resp[:])
			} else {
				conn.writer.Write([]byte{0x88, 0x00})
			}
			return "close frame received"
		}
		shouldProcess := opcode == 2 || opcode == 1
		if shouldProcess == false {

			continue
		}
		if isMask {
			for i := range buf1[:frameLen] {
				buf1[i] = buf1[i] ^ maskBuf[i&3]
			}
		}
		bufWriter.AddPos(frameLen)
		return ""
	}
}

// 写入一个Msg.
func (conn *Conn_t) WriteMsg(buf []byte) (errMsg string) {
	conn.writeCache.Reset()
	headBufLen := 11
	if conn.isWriteMask {
		headBufLen = 15
	}
	writeBuf := conn.writeCache.GetHeadBuffer(len(buf) + headBufLen)
	pos := 0
	writeBuf[pos] = 0x82
	pos++
	length := len(buf)
	b1 := byte(0)
	if conn.isWriteMask {
		b1 = byte(128)
	}
	switch {
	case length >= 65536:
		writeBuf[pos] = b1 | 127
		binary.BigEndian.PutUint64(writeBuf[pos+1:], uint64(length))
		pos += 9
	case length > 125:
		writeBuf[pos] = b1 | 126
		binary.BigEndian.PutUint16(writeBuf[pos+1:], uint16(length))
		pos += 3
	default:
		writeBuf[pos] = b1 | byte(length)
		pos++
	}
	if conn.isWriteMask {
		maskBuf := writeBuf[pos : pos+4]
		errMsg = randDataRead(maskBuf)
		if errMsg != "" {
			return errMsg
		}
		pos += 4
		for i := range buf {
			writeBuf[pos+i] = buf[i] ^ maskBuf[i&3]
		}
		pos += len(buf)
	} else {
		copy(writeBuf[pos:], buf)
		pos += len(buf)
	}
	_, err := conn.writer.Write(writeBuf[:pos])
	if err != nil {
		return err.Error()
	}
	return ""
}

func (conn *Conn_t) Close() error {
	return conn.closer.Close()
}

func randDataRead(buf []byte) (errMsg string) {
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		return "ghs9hk4nzm " + err.Error()
	}
	return ""
}

