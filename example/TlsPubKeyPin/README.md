# TlsPubKeyPin 示例: 锁定服务端证书的公钥

演示怎么通过 `hgmRoomNotify.Client.HttpClient` 给 Go 客户端接管 tls 配置, 做**服务端证书公钥锁定**
(SPKI pinning): **只认那把公钥, 证书链/CA、有效期、域名、吊销状态一概不看。**

## 为什么要这个

本库的 `ClientWsDialReq_t.EnableTlsVerify` 只有两档:

- `false`(默认): 完全不验证证书 —— 谁都能中间人。
- `true`: 走系统信任链的标准验证 —— 需要一张公网 CA 签的、域名对得上、没过期的证书。

内网 / 自研客户端连自研服务端 / 设备连自家云 这类场景里, 第二档的代价往往不划算(要维护 CA 体系、
证书到期要换、内网 IP 没法签)。**公钥锁定是这两档之间那个正确的中间选项**: 客户端和服务端本来就是
同一方运维, 公钥可以带外分发(硬编码进客户端或配置下发), 中间人拿不到那把私钥就冒充不了。
安全性接近甚至强于标准验证(不依赖任何 CA 是否被攻破), 运维成本却低得多。

## 怎么用

三步:

```go
// 1. 带外拿到服务端证书的公钥, 算出 sha256(SPKI) 指纹. 实际项目里这个 [32]byte 硬编码进客户端或配置下发.
var serverPin = [32]byte{0xf4, 0x47, /* ... */}

// 2. 造一个只认这些公钥的 http.Client(实现见 pin.go, 直接抄走即可).
httpClient := NewPubKeyPinHttpClient(serverPin)

// 3. 挂到 hgmRoomNotify 客户端上.
var client hgmRoomNotify.Client
client.HttpClient = httpClient
client.SetWsDialUrl("wss://10.10.10.10:5845/ws")
```

核心就是 `pin.go` 里那段 `tls.Config`:

```go
&tls.Config{
    InsecureSkipVerify: true,  // 关掉标准链验证: 证书链/CA/有效期/域名全不看.
    VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
        cert, err := x509.ParseCertificate(rawCerts[0])  // rawCerts[0] 是叶子证书.
        ...
        // 只比对 sha256(MarshalPKIXPublicKey(cert.PublicKey)).
    },
}
```

`InsecureSkipVerify: true` 在这里**不是**"不验证"—— 它只是把验证从"标准链验证"换成了
`VerifyPeerCertificate` 里的公钥比对。这个回调返回非 nil 时 tls 握手就失败, 连接建立不起来。

## 锁公钥而不是锁整张证书

指纹取的是 **SPKI(公钥)** 的 sha256, 不是整张证书的 sha256。差别在运维上很关键:

- 锁公钥: 服务端换证书(续签、改域名、改有效期、改签发者)只要**私钥不变**, 客户端一行都不用动。
- 锁整证书: 每次续签都要同步更新所有客户端, 漏一个就全断。

真要换私钥时, `NewPubKeyPinHttpClient` 支持传多个 pin —— 新旧公钥同时接受, 等客户端全部升级完再下掉旧的。

## 几个注意点

- **`Client.HttpClient` 非 nil 时 `ClientWsDialReq_t.EnableTlsVerify` 被忽略。** tls 完全由这个
  `http.Client` 的 `Transport.TLSClientConfig` 决定。本例场景2 故意把 `EnableTlsVerify` 设成 `true` 来验证这一点。
- **生命周期归调用者。** 本库只读取和使用这个 `http.Client`, 不持有、不关闭它 —— `CloseForTest` 也不会动它。
  长期运行的进程建一个复用即可; 真要回收时调用者自己 `CloseIdleConnections()`。
- **不用担心 HTTP/2。** websocket 升级只能走 HTTP/1.1, 但 Go 的 `net/http` 已内建处理: 带
  `Connection: upgrade` + `Upgrade: websocket` 的请求被 `Request.requiresHTTP1()` 标成 onlyH1,
  握手时清空 ALPN 且不复用 h2 连接。所以 `ForceAttemptHTTP2` 开不开都不影响。
- 本例只锁**叶子证书**的公钥。要锁中间 CA / 根 CA 的公钥(允许服务端在该 CA 下自由换叶子证书),
  把 `rawCerts` 整条链都遍历一遍比对即可。

## demo 干了什么

进程内起一台真的 wss 服务端, 证书是当场生成的真自签证书, 并且**故意做成三宗罪**:
自签(无可信 CA)+ 已过期一年 + SAN 域名和实际连的 `127.0.0.1` 完全对不上。然后跑三种客户端配置:

| 场景 | 配置 | 结果 | 说明 |
| --- | --- | --- | --- |
| 1 | `EnableTlsVerify=true`, 不设 `HttpClient` | 连不上 | 标准验证被三宗罪挡下(`x509: certificate signed by unknown authority`) |
| 2 | `HttpClient` 锁定正确公钥 | **连上, 收到 FireChange** | 公钥对上就行, 过期/域名/自签全不影响 |
| 3 | `HttpClient` 锁定另一把真密钥的公钥 | 连不上 | 证明锁定确实在生效, 不是摆设 |

场景1 存在的意义: 先证明这张证书**确实是不合格的**, 场景2 才能说明"连上"是公钥锁定放行的,
而不是证书碰巧合法。

## 运行 / 测试

```
cd hgmRoomNotify/example
go run ./TlsPubKeyPin
go test ./TlsPubKeyPin
```

自动测试 `TestTlsPubKeyPin` 跑的是真 tls 握手、真 wss 连接、真 `FireChange`, 除上面三个场景外
还多测一条密钥轮换(pin 列表里含正确公钥就放行)。

## 文件

| 文件 | 内容 |
| --- | --- |
| `pin.go` | 公钥锁定的实现。**要抄走的就是这个文件。** |
| `demoServer.go` | 生成"自签 + 过期 + 域名不符"的真证书, 起真 tls 服务端。 |
| `tryConnect.go` | 用一组给定的 tls 设置连一次并 `FireChange` 一次, 返回结果。 |
| `main.go` | 三个场景的对比演示。 |
| `main_test.go` | 自动测试。 |
