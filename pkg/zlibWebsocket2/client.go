package zlibWebsocket2

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

type Client_ctx_t struct {
	EnableTlsVerify bool
	Url             string
	ReqHeader       http.Header
	RespHeader      http.Header
	Conn            Conn_t
	CloseContext    context.Context
	ErrMsg          string
}

func (ctx *Client_ctx_t) Call() {
	u, err := url.Parse(ctx.Url)
	if err != nil {
		ctx.ErrMsg = err.Error()
		return
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
	default:
		ctx.ErrMsg = "unexpected url scheme: " + u.Scheme
		return
	}
	if ctx.ReqHeader == nil {
		ctx.ReqHeader = http.Header{}
	}
	var p [16]byte
	_, err = io.ReadFull(rand.Reader, p[:])
	if err != nil {
		ctx.ErrMsg = "rand.Reader fail " + err.Error()
		return
	}
	key := base64.StdEncoding.EncodeToString(p[:])
	if ctx.CloseContext == nil {
		ctx.CloseContext = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx.CloseContext, "GET", u.String(), nil)
	if err != nil {
		ctx.ErrMsg = err.Error()
		return
	}
	req.Header = ctx.ReqHeader
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	httpClient := getHttpClient(ctx.EnableTlsVerify)
	resp, err := httpClient.Do(req)
	if err != nil {
		ctx.ErrMsg = err.Error()
		return
	}
	var rwc io.ReadWriteCloser
	isHttpOk := func() bool {
		if resp.StatusCode != 101 {
			return false
		}
		if strings.ToLower(resp.Header.Get("Upgrade")) != "websocket" {
			return false
		}
		if strings.ToLower(resp.Header.Get("Connection")) != "upgrade" {
			return false
		}
		if resp.Header.Get("Sec-WebSocket-Accept") != computeAcceptKey(key) {
			return false
		}
		var ok bool
		rwc, ok = resp.Body.(io.ReadWriteCloser)
		if ok == false {
			return false
		}
		return true
	}()
	if isHttpOk == false {
		defer resp.Body.Close()
		buf := make([]byte, 100)
		nr, _ := resp.Body.Read(buf)
		ctx.ErrMsg = "httpNoWebsocket " + strconv.Itoa(resp.StatusCode) + " " + string(buf[:nr])
		return
	}
	ctx.RespHeader = resp.Header
	ctx.Conn.writer = rwc
	ctx.Conn.closer = rwc
	ctx.Conn.reader = rwc
	ctx.Conn.isWriteMask = true
}

var gkeyGUID = []byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11")

func computeAcceptKey(challengeKey string) string {
	h := sha1.New()
	h.Write([]byte(challengeKey))
	h.Write(gkeyGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

var gSkipTlsClient *http.Client

var gSkipTlsClientOnce sync.Once

func getHttpClient(EnableTlsVerify bool) *http.Client {
	if EnableTlsVerify {
		return http.DefaultClient
	}
	gSkipTlsClientOnce.Do(func() {
		gSkipTlsClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		}
	})
	return gSkipTlsClient
}

