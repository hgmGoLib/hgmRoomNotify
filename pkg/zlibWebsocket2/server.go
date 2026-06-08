package zlibWebsocket2

import (
	"net/http"
	"strings"
	"time"
)

type Server_ctx_t struct {
	R      *http.Request
	W      http.ResponseWriter
	Conn   Conn_t
	ErrMsg string
}

func (ctx *Server_ctx_t) Call() {
	isHttpOk := func() bool {
		if ctx.R.Method != "GET" {
			return false
		}
		if strings.EqualFold(ctx.R.Header.Get("Connection"), "upgrade") == false {
			return false
		}
		if strings.EqualFold(ctx.R.Header.Get("Upgrade"), "websocket") == false {
			return false
		}
		if strings.EqualFold(ctx.R.Header.Get("Sec-WebSocket-Version"), "13") == false {
			return false
		}
		if ctx.R.Header.Get("Sec-WebSocket-Key") == "" {
			return false
		}
		return true
	}()
	if isHttpOk == false {
		ctx.ErrMsg = "req is not websocket"
		return
	}
	hijacker, ok := ctx.W.(http.Hijacker)
	if ok == false {
		ctx.ErrMsg = "W is not Hijacker"
		return
	}
	ctx.W.Header().Set("Upgrade", "websocket")
	ctx.W.Header().Set("Connection", "Upgrade")
	ctx.W.Header().Set("Sec-WebSocket-Accept", computeAcceptKey(ctx.R.Header.Get("Sec-WebSocket-Key")))
	ctx.W.WriteHeader(101)
	conn, _, err := hijacker.Hijack()
	if err != nil {
		ctx.ErrMsg = "Hijack fail " + err.Error()
		return
	}
	err = conn.SetDeadline(time.Time{})
	if err != nil {
		ctx.ErrMsg = "SetDeadline fail " + err.Error()
		return
	}
	ctx.Conn.reader = conn
	ctx.Conn.writer = conn
	ctx.Conn.closer = conn
	ctx.Conn.isWriteMask = false
}

