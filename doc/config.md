# 可配置参数

下面列出本 package 对外可配置的全部参数及其效果。回调类字段(`OnAcceptFn`/`OnAllowFn`/`OnDenyFn`/`ObsFn`/`WsDialReqFn`)见各自类型注释, 这里只列数值/行为类配置。

## `ServerManager`(服务端)

| 字段 | 类型 | 默认 | 上限 | 配置效果 |
| --- | --- | --- | --- | --- |
| `RoomEnterMaxPerConn` | `int` | `1024` | 无 | 单条连接最多能进入的房间数。超过时给客户端发 `deny`(roomOverLimit)并断开该连接。`0`=默认, 负值 `_init` panic。 |
| `WriteBufMaxBytes` | `int` | `64KB` | `64MB` | 每条连接的写缓冲最大字节数(环形缓冲)。缓冲写满(慢客户端)时服务端主动断开**那一条**连接, 不阻塞其它连接的广播。`0`=默认, 负值/超限 `_init` panic。调大后客户端 `ReadMsgMaxBytes` 必须 ≥ 本值。 |
| `LiveDataMaxSize` | `int` | `1024` | `16MB`, 且 ≤ `WriteBufMaxBytes` 的 25% | 单次 `FireChange` 携带的 `LiveData` 最大字节数。超过则该次 `LiveData` 静默丢弃(发 `serverLiveDataDropped` obs), 但变更通知仍照常下发(客户端收到空 `LiveData` 走 ajax 补取)。`0`=默认, 负值/超限/超 25% `_init` panic。超过单 frame(约 64KB)时自动用 `roomValueMore`...`roomValue` 分块传输。 |

> 上述三个字段必须在首次调用 `ServeHTTP`/`FireChange` 之前设置好, 之后不再读取也不可更改(无锁/无 atomic, 并发安全靠此文档约束; `_init` 会把默认值写回字段本身, 之后所有读取点直接读字段)。

## `Client`(客户端)

| 字段 | 类型 | 默认 | 配置效果 |
| --- | --- | --- | --- |
| `ReadMsgMaxBytes` | `int` | `64KB` | 单个 websocket message 最大读取字节数, 同时也是分块 `LiveData` 重组缓冲上限。**必须 ≥ 服务端 `WriteBufMaxBytes`**(单个 message 最大可达该值), 否则会因消息过大断开。`0`=默认, 负值在客户端 `_init` 时 panic(默认值写回字段本身)。 |
| `HttpClient` | `*http.Client` | `nil` | 发起 websocket 升级请求(含 tls 握手)用的 `http.Client`。`nil`=用本库内置的, 按 `ClientWsDialReq_t.EnableTlsVerify` 决定验不验证 tls 证书。非 `nil` 时**完全以本字段为准**, `EnableTlsVerify` 被忽略(tls 由本 `http.Client` 的 `Transport.TLSClientConfig` 决定)。用途: 自定义 `tls.Config`(公钥锁定 / 自定义 CA / 双向认证)、http 代理、自定义 dialer。详见下方说明。 |

### `HttpClient` 的两条约束

**生命周期完全归调用者。** 本库只读取和使用它, 不持有、不关闭 —— `CloseForTest` 也不会动它。长期运行的进程建一个复用即可; 真要回收时调用者自己 `CloseIdleConnections()`。这样设计是为了避开"每次重连造一个 `http.Client` 导致 transport 泄漏"的坑: 本库不替调用者猜实例该活多久。

**不用担心 HTTP/2。** websocket 升级只能走 HTTP/1.1(依赖 101 Switching Protocols), 但 Go 的 `net/http` 已经内建处理: 带 `Connection: upgrade` + `Upgrade: websocket` 的请求被 `Request.requiresHTTP1()` 标成 onlyH1, `persistConn.addTLS` 握手前清空 `NextProtos`(不发 ALPN 扩展), `useRegisteredProtocol` 也不让它复用已缓存的 h2 连接。所以标准 `*http.Transport` 随便传, `ForceAttemptHTTP2` 开着(`http.DefaultTransport` 就是)也照样连得上。只有塞进一个只会 h2 的自定义 `RoundTripper`(如直接用 `golang.org/x/net/http2.Transport`)才会连不上。

这条行为有自动测试兜底(测试在作者的私有集成测试工程里, 未随本开源仓库发布): 用一台 ALPN 广播 `["h2","http/1.1"]` 的标准 tls 服务端, 同一个 `ForceAttemptHTTP2=true` 的 transport 发普通请求走 HTTP/2、连 websocket 走 HTTP/1.1 且正常收发。

本字段必须在第一次 `RoomEnter` 之前设置好, 之后不再修改(无锁保护, 每次(重)连时直接读本字段)。

用法完整例子见 [`example/TlsPubKeyPin/`](../example/TlsPubKeyPin/)(锁定服务端证书公钥)。

## `TimeoutCfg_t`(超时配置, 两端共用)

`ServerManager.TimeoutCfg` 与 `Client.TimeoutCfg` 都是这个类型。其中 `ClientXxx` 字段由服务端通过 `setTimeCfg` 消息下发给客户端(服务端配置覆盖客户端本地配置), `ServerXxx` 字段仅服务端使用(不参与序列化)。所有字段 `InitWithDefault` 时会被下限钳到 ≥ 100ms。

| 字段 | 默认 | 配置效果 |
| --- | --- | --- |
| `ClientReconnectMinDur` | `5s` | 客户端两次发起连接之间的最小间隔(限制重连频率)。 |
| `ClientIdleToSendKeepAliveDur` | `4.5s` | 客户端网络 idle(最后收/发包的较小值)达到此值就发 keepalive ping。会被自动钳到 ≤ `ClientLastReadToReconnectDur/2`。 |
| `ClientLastReadToReconnectDur` | `10s` | 客户端最后收包时间超过此值就关闭连接并重连。 |
| `ClientWsDialTimeoutDur` | `5s` | 客户端 `websocket.Dial`(含 tcp+http 握手)的最大等待时间。 |
| `ClientNoNeedIdleDur` | `20s` | 客户端没有任何房间需求后, 保持连接多久才关闭(避免频繁断连重连)。 |
| `ClientLastReadToUiNoWorkDur` | `10s` | 客户端最后收包时间超过此值, `GetUiStatusToUser` 显示为离线。 |
| `ServerLastReadToCloseDur` | `120s` | 服务端最后收包时间超过此值就关闭该连接(客户端 keepalive 跟不上即被判死)。 |
| `ServerAuthTimeoutDur` | `30s` | 服务端启用认证(`OnAllowFn!=nil`)时, 未认证连接的超时关闭时间。 |
| `ServerAskStopListenTimeoutDur` | `30s` | 服务端发 askStopListen 后等待客户端断开的超时时间。 |
