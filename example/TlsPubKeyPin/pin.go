package main

// 服务端证书公钥锁定(SPKI pinning).
//
// 做法: 关掉标准 tls 链验证(InsecureSkipVerify=true), 改用 VerifyPeerCertificate 回调,
// 只比对服务端叶子证书里的公钥指纹, 其它一概不看 —— 不看证书链/签发 CA, 不看有效期(过期照连),
// 不看域名/SAN 是否匹配, 不看吊销状态.
//
// 适用: 客户端和服务端是同一方运维、能带外分发公钥指纹的场景(内网、自研客户端连自研服务端、
// 设备连自家云)。这种场景下搞正经 CA 体系往往不划算, 而公钥锁定比"干脆不验证"强得多:
// 中间人拿不到那把私钥就冒充不了。
//
// 注意: 锁的是公钥而不是整张证书, 所以服务端换证书(续签、改域名、改有效期)只要**私钥不变**,
// 客户端不用动。真要换私钥时, 用多个 pin 同时接受新旧两把, 等客户端全部升级完再下掉旧的。

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
)

// 公钥(SPKI DER 编码)的 sha256 指纹. 只含公钥本身, 不含有效期/域名/签发者等任何其它信息.
// pub 传 crypto 公钥对象, 例如 x509 证书的 cert.PublicKey, 或 ecdsaPrivKey.PublicKey 的地址.
func PubKeyPinSha256(pub any) ([32]byte, error) {
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(spki), nil
}

// 只认 wantPinList 里任一公钥的 http.Client, 直接赋给 hgmRoomNotify.Client.HttpClient 即可.
// 传多个 pin 是为了密钥轮换: 新旧两把公钥同时接受, 等客户端全部升级完再下掉旧的.
//
// 生命周期由调用者负责: hgmRoomNotify 不会关闭它. 长期运行的进程一个 client 复用即可;
// 用完(比如整个功能下线)时调用者自己 CloseIdleConnections.
func NewPubKeyPinHttpClient(wantPinList ...[32]byte) *http.Client {
	return &http.Client{
		// 不用管 HTTP/2: websocket 升级请求会被 net/http 的 Request.requiresHTTP1() 锁死在 HTTP/1.1 上
		// (清空 ALPN + 不复用 h2 连接), 所以 ForceAttemptHTTP2 开不开都不影响.
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				// 关掉标准链验证: 证书链/CA/有效期/域名全都不看.
				InsecureSkipVerify: true,
				// 改成只比对公钥指纹. 返回 nil 表示接受, 返回 error 表示握手失败.
				VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
					if len(rawCerts) == 0 {
						return errors.New("tls pubKeyPin: 服务端没有发来证书")
					}
					// rawCerts[0] 是叶子证书(服务端自己的证书), 后面是中间 CA. 这里只锁叶子.
					cert, err := x509.ParseCertificate(rawCerts[0])
					if err != nil {
						return err
					}
					gotPin, err := PubKeyPinSha256(cert.PublicKey)
					if err != nil {
						return err
					}
					for _, wantPin := range wantPinList {
						if gotPin == wantPin {
							return nil
						}
					}
					return errors.New("tls pubKeyPin: 服务端证书公钥不在锁定列表里")
				},
			},
		},
	}
}
