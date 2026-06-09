package zlibWebsocket2

import (
	"bytes"
	"net"
	"testing"

	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibBytes"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibVnet"
)

// 验证 WriteFrame 的前置留空(prefix)原地写帧头路径: 写端用带 prefix 的 FrameBuf 发送,
// 读端 ReadFrame 解出 payload, 两侧字节必须完全一致. 覆盖 掩码/不掩码 与 小/中 体积.
func TestWriteFrame_InPlace_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		isMask  bool
		payload []byte
	}{
		{"unmask_small", false, bytes.Repeat([]byte("x"), 50)},
		{"unmask_medium", false, bytes.Repeat([]byte("y"), 1000)},
		{"mask_small", true, bytes.Repeat([]byte("z"), 50)},
		{"mask_medium", true, bytes.Repeat([]byte("w"), 1000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cw, cr := net.Pipe()
			defer cw.Close()
			defer cr.Close()
			wconn := &Conn_t{writer: cw, isWriteMask: tc.isMask}
			rconn := &Conn_t{reader: cr, writer: cr}

			// 写端: 在 payload 前留出 prefix 字节, WriteFrame 应走原地写头(零 copy).
			prefix := int(wconn.GetFrameBufPreservedSize().Prefix)
			buf := make([]byte, prefix+len(tc.payload))
			copy(buf[prefix:], tc.payload)
			fb := zlibVnet.FrameBuf{Buf: buf, StartPos: uint16(prefix)}

			errCh := make(chan error, 1)
			go func() { errCh <- wconn.WriteFrame(&fb) }()

			var rfb zlibVnet.FrameBuf
			if err := rconn.ReadFrame(&rfb); err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if err := <-errCh; err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			got := rfb.Buf[rfb.StartPos:]
			if !bytes.Equal(got, tc.payload) {
				t.Fatalf("payload mismatch: got %d bytes, want %d", len(got), len(tc.payload))
			}
		})
	}
}

// 无前置留空(StartPos=0)时应退回 WriteMsg copy 路径, 结果仍正确.
func TestWriteFrame_NoPrefix_Fallback(t *testing.T) {
	cw, cr := net.Pipe()
	defer cw.Close()
	defer cr.Close()
	wconn := &Conn_t{writer: cw, isWriteMask: false}
	rconn := &Conn_t{reader: cr, writer: cr}

	payload := []byte("no-prefix-fallback-path")
	var w zlibBytes.BufWriter
	w.Write_(payload)
	fb := zlibVnet.FrameBuf{Buf: w.GetBytes(), StartPos: 0}

	errCh := make(chan error, 1)
	go func() { errCh <- wconn.WriteFrame(&fb) }()

	var rfb zlibVnet.FrameBuf
	if err := rconn.ReadFrame(&rfb); err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	if !bytes.Equal(rfb.Buf[rfb.StartPos:], payload) {
		t.Fatal("fallback payload mismatch")
	}
}
