package hgmRoomNotify

import (
	"sync"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibChannel"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibVnet"
)

// 服务端连接的写入缓冲. 使用Frame16BipBuf环形缓冲区.
// 调用者序列化消息到缓冲区(uint16长度前缀由BipBuf自动写入), 写入线程取出连续块作为websocket message发送.
// 没有写入需求时没有写入goroutine, 有需求时异步启动.
type server_conn_write_buf_t struct {
	mu            sync.Mutex
	bipBuf        zlibChannel.Frame16BipBuf2
	writerRunning bool      // 当前是否有写入goroutine在运行
	isBroken      bool      // 连接已断开, 拒绝新的push
	sconn         *server_conn_t
	fb            zlibVnet.FrameBuf // writeLoop 复用的 FrameBuf, 避免每帧 &fb 逃逸分配. 同一时刻只有一个 writeLoop, 无需加锁.
}

type pushMsgResult_t uint8
const pushMsgResult_ok pushMsgResult_t = 0
const pushMsgResult_msgTooLarge pushMsgResult_t = 1
const pushMsgResult_broken pushMsgResult_t = 2
const pushMsgResult_bufFull pushMsgResult_t = 3

// 将消息序列化并追加到写入缓冲. 返回错误码表示失败原因.
func (wb *server_conn_write_buf_t) pushMsg(msg Msg_t) pushMsgResult_t {
	msgSize, errMsg := msg.BinarySize()
	if errMsg != "" {
		return pushMsgResult_msgTooLarge
	}
	if msgSize > 4096 {
		return pushMsgResult_msgTooLarge
	}
	wb.mu.Lock()
	if wb.isBroken {
		wb.mu.Unlock()
		return pushMsgResult_broken
	}
	buf := wb.bipBuf.AllocFrame(uint16(msgSize))
	if buf == nil {
		wb.mu.Unlock()
		return pushMsgResult_bufFull
	}
	msg.MarshalBinaryInto(buf)
	if !wb.writerRunning {
		wb.writerRunning = true
		go wb.writeLoop()
	}
	wb.mu.Unlock()
	return pushMsgResult_ok
}

// 标记连接已断开, 拒绝新的push.
func (wb *server_conn_write_buf_t) markBroken() {
	wb.mu.Lock()
	wb.isBroken = true
	wb.writerRunning = false
	wb.mu.Unlock()
}

// 写入线程. 取出连续块作为websocket message发送.
// 缓冲区清空后退出, 下次pushMsg有数据时再启动新的writeLoop.
func (wb *server_conn_write_buf_t) writeLoop() {
	for {
		wb.mu.Lock()
		if wb.isBroken {
			wb.writerRunning = false
			wb.mu.Unlock()
			return
		}
		sendData := wb.bipBuf.TakeSendSlice()
		if sendData == nil {
			wb.writerRunning = false
			wb.mu.Unlock()
			return
		}
		wb.mu.Unlock()

		if wb.sconn.conn.closer.IsClose() {
			wb.markBroken()
			return
		}
		// sendData 前 Prefix 字节是给 websocket 帧头预留的 scratch, payload 从 StartPos=Prefix 开始.
		// WriteFrame 把帧头原地写进这段预留区, 零 payload copy.
		wb.fb.Buf = sendData
		wb.fb.StartPos = uint16(wb.bipBuf.Prefix)
		err := wb.sconn.conn.raw.WriteFrame(&wb.fb)
		if err != nil {
			wb.markBroken()
			if !wb.sconn.conn.closer.IsClose() {
				if wb.sconn.conn.onLogClose != nil {
					wb.sconn.conn.onLogClose(CloseReason_writeFail, "writeLoop "+err.Error())
				}
				wb.sconn.conn.closer.Close2()
			}
			return
		}
		if wb.sconn.conn.onWriteFinishSucc != nil {
			wb.sconn.conn.onWriteFinishSucc()
		}
	}
}
