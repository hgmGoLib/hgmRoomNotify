package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"strings"
	"testing"
)

// 真 tls 服务端(真自签证书: 已过期 + 域名对不上 + 无可信 CA) + 真 wss 连接, 验证公钥锁定的三条行为:
//  1. 标准 tls 验证连不上(证书本身确实是不合格的, 保证下面两条不是因为证书碰巧合法才通过).
//  2. 锁定正确公钥连得上, 并且收得到 FireChange —— 即公钥锁定不看有效期/域名/CA.
//  3. 锁定另一把真密钥的公钥连不上 —— 即锁定确实在生效.
func TestTlsPubKeyPin(t *testing.T) {
	server := startDemoTlsServer()
	defer server.CloseFn()
	serverPin, err := PubKeyPinSha256(server.PubKey)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 标准 tls 验证: 应当连不上.
	resp1 := tryConnect(tryConnectReq_t{Server: server, EnableTlsVerify: true})
	if resp1.IsConnectedSucc {
		t.Fatal("标准 tls 验证不应该接受这张自签且已过期且域名不匹配的证书")
	}
	if strings.Contains(resp1.LastDialFailMsg, "x509") == false {
		t.Fatalf("失败原因应当是 x509 证书验证失败, 实际: %s", resp1.LastDialFailMsg)
	}

	// 2. 锁定正确公钥: 应当连上并收到 FireChange. 同时 EnableTlsVerify=true 应被忽略.
	httpClient2 := NewPubKeyPinHttpClient(serverPin)
	defer httpClient2.CloseIdleConnections()
	resp2 := tryConnect(tryConnectReq_t{Server: server, HttpClient: httpClient2, EnableTlsVerify: true})
	if resp2.IsConnectedSucc == false {
		t.Fatalf("锁定正确公钥应当连得上, 失败原因: %s", resp2.LastDialFailMsg)
	}
	if resp2.GotFireChangeNum != 1 {
		t.Fatalf("连上后应当收到 1 次 FireChange 通知, 实际 %d 次", resp2.GotFireChangeNum)
	}

	// 3. 锁定另一把真密钥的公钥: 应当连不上.
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPin, err := PubKeyPinSha256(&otherKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	httpClient3 := NewPubKeyPinHttpClient(otherPin)
	defer httpClient3.CloseIdleConnections()
	resp3 := tryConnect(tryConnectReq_t{Server: server, HttpClient: httpClient3})
	if resp3.IsConnectedSucc {
		t.Fatal("锁定错误公钥不应该连得上")
	}
	if strings.Contains(resp3.LastDialFailMsg, "pubKeyPin") == false {
		t.Fatalf("失败原因应当是公钥锁定不通过, 实际: %s", resp3.LastDialFailMsg)
	}

	// 4. 多个 pin(密钥轮换场景): 列表里有正确公钥就应当连得上.
	httpClient4 := NewPubKeyPinHttpClient(otherPin, serverPin)
	defer httpClient4.CloseIdleConnections()
	resp4 := tryConnect(tryConnectReq_t{Server: server, HttpClient: httpClient4})
	if resp4.IsConnectedSucc == false {
		t.Fatalf("pin 列表里含正确公钥时应当连得上, 失败原因: %s", resp4.LastDialFailMsg)
	}
}
