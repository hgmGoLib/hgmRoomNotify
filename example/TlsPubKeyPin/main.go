// 演示怎么给 hgmRoomNotify 的 Go 客户端锁定服务端证书的公钥(SPKI pinning):
// 只认那把公钥, 证书链/CA、有效期、域名 一概不看.
//
// 进程内起一台真的 wss 服务端, 它的证书是当场生成的真自签证书, 而且故意做成
// "已过期 + 域名对不上 + 没有可信 CA 签发". 然后跑三种客户端配置对比:
//
//	1. 标准 tls 验证(EnableTlsVerify=true, 不设 HttpClient) -> 连不上(证书三宗罪)。
//	2. 公钥锁定(Client.HttpClient 设成只认那把公钥的 http.Client) -> 连得上, 收得到 FireChange。
//	3. 锁定另一把公钥 -> 连不上(证明确实在验, 不是摆设)。
//
// 运行: cd hgmRoomNotify/example && go run ./TlsPubKeyPin
// 自动测试: cd hgmRoomNotify/example && go test ./TlsPubKeyPin
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"log"
)

func main() {
	server := startDemoTlsServer()
	defer server.CloseFn()
	fmt.Printf("demo wss 服务端: %s (证书: 自签 + 已过期 + 域名对不上)\n\n", server.Url)

	// 运维带外拿到服务端公钥后算出的指纹. 实际项目里这个 [32]byte 常量硬编码进客户端, 或者由配置下发.
	serverPin, err := PubKeyPinSha256(server.PubKey)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("服务端公钥 pin(sha256 of SPKI): %x\n\n", serverPin)

	// 场景1: 标准 tls 验证. 自签 + 过期 + 域名不匹配, 任意一条都足以让标准验证失败.
	resp1 := tryConnect(tryConnectReq_t{Server: server, EnableTlsVerify: true})
	fmt.Printf("场景1 标准tls验证:   连上=%v  收到FireChange=%d\n", resp1.IsConnectedSucc, resp1.GotFireChangeNum)
	fmt.Printf("         失败原因: %s\n\n", resp1.LastDialFailMsg)
	if resp1.IsConnectedSucc {
		log.Fatal("场景1 本该连不上")
	}

	// 场景2: 公钥锁定. 只比对公钥 -> 过期/域名/自签全都不影响, 连得上.
	resp2 := tryConnect(tryConnectReq_t{
		Server:     server,
		HttpClient: NewPubKeyPinHttpClient(serverPin),
		// 故意打开: HttpClient 非 nil 时本字段被忽略, tls 完全由 HttpClient 的 Transport 决定.
		EnableTlsVerify: true,
	})
	fmt.Printf("场景2 锁定正确公钥:  连上=%v  收到FireChange=%d\n\n", resp2.IsConnectedSucc, resp2.GotFireChangeNum)
	if resp2.IsConnectedSucc == false || resp2.GotFireChangeNum != 1 {
		log.Fatalf("场景2 本该连上并收到 1 次变更, 实际 连上=%v 收到=%d 失败原因=%s",
			resp2.IsConnectedSucc, resp2.GotFireChangeNum, resp2.LastDialFailMsg)
	}

	// 场景3: 锁定另一把真密钥的公钥 -> 指纹对不上, 握手失败.
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	otherPin, err := PubKeyPinSha256(&otherKey.PublicKey)
	if err != nil {
		log.Fatal(err)
	}
	resp3 := tryConnect(tryConnectReq_t{Server: server, HttpClient: NewPubKeyPinHttpClient(otherPin)})
	fmt.Printf("场景3 锁定错误公钥:  连上=%v  收到FireChange=%d\n", resp3.IsConnectedSucc, resp3.GotFireChangeNum)
	fmt.Printf("         失败原因: %s\n\n", resp3.LastDialFailMsg)
	if resp3.IsConnectedSucc {
		log.Fatal("场景3 本该连不上")
	}

	fmt.Println("demo done.")
}
