package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"time"

	"github.com/hgmGoLib/hgmRoomNotify"
)

type demoServer_t struct {
	WsServer *hgmRoomNotify.ServerManager
	Url      string // wss://127.0.0.1:<随机端口>/ws
	// 服务端证书里的公钥. 实际部署中这个东西由运维带外分发给客户端(硬编码进客户端 / 配置下发),
	// 客户端算出它的 sha256 指纹作为 pin. 这里为了 demo 自包含, 直接从内存里拿.
	PubKey  any
	CloseFn func()
}

// 起一台真的 tls 服务端, 跑 hgmRoomNotify 的 /ws.
//
// 证书是当场生成的真自签证书, 并且故意做成三种"标准验证必然失败"的样子:
//   - 自签, 没有任何可信 CA 签发;
//   - 已经过期(NotAfter 在一年前);
//   - 域名/IP 完全对不上(SAN 里只有一个不存在的域名, 而客户端连的是 127.0.0.1)。
//
// 公钥锁定不看这三样里的任何一样, 所以照样连得上 —— 这正是本例子要演示的点。
func startDemoTlsServer() demoServer_t {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pubkey-pin-demo.example.invalid"},
		NotBefore:    now.AddDate(-2, 0, 0),
		NotAfter:     now.AddDate(-1, 0, 0), // 一年前就过期了.
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"pubkey-pin-demo.example.invalid"}, // 故意不含 127.0.0.1.
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	tlsCert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	wsServer := &hgmRoomNotify.ServerManager{}
	mux := http.NewServeMux()
	mux.Handle("/ws", wsServer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	srv := &http.Server{
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{tlsCert}},
		// 这里不动 HTTP/2 相关设置, 就是最普通的 tls http.Server —— ServeTLS 会自动装上 HTTP/2,
		// ALPN 广播 ["h2","http/1.1"]. websocket 升级请求会被 net/http 自动锁在 HTTP/1.1 上, 不受影响.
		// 本 demo 故意制造 tls 握手失败(场景1/场景3), 服务端会把每次失败都打到 stderr, 和 demo 输出混在一起.
		// 这里关掉只是为了输出干净, 真实项目应该留着默认行为.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() {
		_ = srv.ServeTLS(ln, "", "")
	}()
	return demoServer_t{
		WsServer: wsServer,
		Url:      "wss://" + ln.Addr().String() + "/ws",
		PubKey:   &key.PublicKey,
		CloseFn: func() {
			_ = srv.Close()
		},
	}
}
