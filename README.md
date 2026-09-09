# hgmRoomNotify

基于 WebSocket 的房间状态变更通知框架。服务端维护若干"房间", 数据变更时通知所有订阅了该房间的客户端。

核心是低延迟的"变更通知 + ajax 拉取": ws 戳一下告诉客户端某个房间变了, 真实数据和可靠性由业务自己的数据库 + ajax 兜底。
在此之上, 通知里可以**顺带捎上**两个加速字段, 命中就省一次 ajax 往返(RTT):

- `CVersionId`(≤100 字节, 服务端存当前值): 直接装得下的**短状态量**——如"是否在线"、typing、未读数、数据版本号——
  客户端 `onChange` 读到就能直接更新, 进房/重连还会自动下发当前值, **常常完全不必再 ajax**。
- `LiveData`(默认 ≤1024 字节, 仅实时传一次): 一条**一次性增量**——如新聊天消息、streaming 文本片段——命中就直接用, 省掉拉取。

两者都是**尽力而为的加速缓存**, 不是可靠传输: 没命中(没带/超限/缓冲满/重连/重启)时退回 ajax 拉全量即可。
所以框架既不是"只能戳一下", 也不是"可靠推内容"——它是"戳一下 + 顺带捎点数据省 RTT, 拉取兜底"。

## 功能概览

1. 服务端维护若干"房间"(Room), 每个房间有一个字符串 id。
2. 客户端通过 WebSocket 连接后, 可以加入/离开房间。
3. 服务端调用 `FireChange(roomId)` 时, 所有订阅了该房间的客户端会收到变更通知。
4. 变更通知携带以下信息:
   - `RoomEpoch`+`ChangeSeq`: 框架自动维护。`RoomEpoch` 是房间纪元 id(房间创建时生成, 房间被重建或服务器重启都会变化), `ChangeSeq` 在同一 `RoomEpoch` 下每次 `FireChange` 递增; 客户端用 `(RoomEpoch, ChangeSeq)` 判断是否有新变化并检测房间重建/服务器重启。
   - `CVersionId`: 调用者自定义的版本 id, 最大 100 字节。服务端内存**单值**存储(只存最新一个), 可选。
     和 `LiveData` 一样**不保证总是有**: 没带的 `FireChange` 会把它清空、服务器重启会丢失、进未变更过的房间为空。
     只能当"省一次拉取"的机会主义提示, 不能当跳过全量的唯一判据, 详见 [`doc/accelFieldsNotReliable.md`](doc/accelFieldsNotReliable.md)。
   - `LiveData`: 本次变更的附加数据, 默认最大 1024 字节(可调)。服务端不存储, 仅实时传递, 可选; 网络断线重连或服务器重启会丢失。
   - 两者如何选: **当前状态量**("是什么", 如在线/typing、未读数、数据版本号)用 `CVersionId`(存当前值, 进房/重连自动下发, 丢一次会自动收敛, 必要时 ajax 保底);
     **一次性增量**("发生了什么", 如新消息、streaming 片段)用 `LiveData`(尽力而为, 丢了走 ajax 补)。详见下文[工作模式](#工作模式-通知--拉取)。
5. 客户端断线后自动重连, 重连后自动重新加入所有之前的房间, 并收到当前版本号。
6. 认证(可选): 服务端配置 `OnAllowFn` 回调, 同时处理连接级和房间级准入(`ctx.RoomId==""` 为连接级)。
   不配置则全部放行(等同于无认证)。客户端发送 in-band identity(类似 sessionId/token, 本模块不解析),
   也可由网络层(ws cookie/url query)经 `OnAcceptFn` 提供网络层 sessionId。两个身份来源互相独立。
7. 运行时撤权/踢下线: 服务端调用 `conn.CloseConn(isTemp, reason)` 或 `CloseConnBySessionId(sessionId, isTemp, reason)`。
   `isTemp=true` 客户端会重连(重连重新过 `OnAllowFn`, 用于撤权); `isTemp=false` 客户端不再重连(永久封禁/下线)。
8. 客户端配置 `OnDenyFn` 处理被拒(连接级/房间级)。不配置时遇到 deny 视为对接错误(断开且不再重连, 应修复 bug)。
9. 提供 Go 客户端和浏览器 TypeScript 客户端两套实现。

## 快速开始

### 服务端 (Go)

1. 创建 `ServerManager` 实例, 配置超时参数和认证回调:

   ```go
   var wsServer hgmRoomNotify.ServerManager
   wsServer.OnAcceptFn = func(ctx *hgmRoomNotify.ServerOnAccept_ctx_t) {
       // 从 ctx.R 读 cookie 做认证
       ctx.SessionId = "userId"
   }
   ```

2. 将 `ServerManager` 注册为 `http.Handler`(它实现了 `ServeHTTP`):

   ```go
   mux := http.NewServeMux()
   mux.Handle("/ws", &wsServer)
   httpServer := httptest.NewServer(mux) // 生产环境用 http.ListenAndServe(addr, mux)
   defer httpServer.Close()
   ```

   也可在已有框架中对接: 拿到 `http.ResponseWriter` 和 `*http.Request` 后调用 `wsServer.ServeHTTP(w, r)`。

3. 数据变更时调用 `FireChange`:

   ```go
   wsServer.FireChange(hgmRoomNotify.RoomEvent_t{
       RoomId:     "order:12345",
       CVersionId: "v3",                          // 可选
       LiveData:   []byte(`{"Status":"paid"}`),   // 可选, 默认最大 1024 字节
   })
   ```

   所有订阅了 `"order:12345"` 这个房间的客户端都会收到通知。

### 浏览器客户端 (TypeScript)

1. 创建客户端实例并设置 URL:

   ```ts
   import { hgmRn_Client } from "./hgmRoomNotifyBrowserTs/index.ts"
   const client = new hgmRn_Client()
   client.setUrl("/ws")  // 会自动根据当前页面协议转为 wss:// 或 ws://
   ```

2. 加入房间并监听变更:

   ```ts
   const leaveFn = client.roomEnter("order:12345", (ev) => {
       // ev.RoomId      房间 id
       // ev.RoomEpoch   房间纪元 id(房间重建/服务器重启会变化, 通常不需要关心)
       // ev.ChangeSeq   变化序号(用于去重, 通常不需要关心)
       // ev.CVersionId  自定义版本号
       // ev.LiveData    Uint8Array|null, 附加数据
       console.log("房间变更", ev.CVersionId)
       // 这里发 ajax 请求获取最新数据...
   })
   ```

3. 不再需要时离开房间:

   ```ts
   leaveFn()  // 取消订阅。所有房间都离开后连接会在 20 秒后自动关闭。
   ```

### Go 客户端

1. 创建客户端实例:

   ```go
   var client hgmRoomNotify.Client
   client.SetWsDialUrl("wss://example.com/ws")
   ```

2. 加入房间:

   ```go
   leaveFn := client.RoomEnter("order:12345", func(ev *hgmRoomNotify.RoomOnChange_t) {
       // ev.RoomId, ev.RoomEpoch, ev.ChangeSeq, ev.CVersionId, ev.LiveData
   })
   ```

3. 离开房间:

   ```go
   leaveFn()
   ```

完整可运行的 Go 端 demo(服务端 + 客户端在一个进程里跑起来)见 [`example/SimpleDemo/`](example/SimpleDemo/),
运行 `cd example && go run ./SimpleDemo`。全部例子见[示例](#示例)。

#### 自定义 tls / 代理: `Client.HttpClient`

`ClientWsDialReq_t.EnableTlsVerify` 只有"完全不验证"和"走系统信任链标准验证"两档。需要别的 tls 行为
(公钥锁定、自定义 CA、双向认证)或者要走 http 代理 / 自定义 dialer 时, 给 `Client.HttpClient` 挂一个自己的
`http.Client`:

```go
var client hgmRoomNotify.Client
client.HttpClient = &http.Client{
    Transport: &http.Transport{TLSClientConfig: myTlsConfig},
}
client.SetWsDialUrl("wss://example.com/ws")
```

- 非 `nil` 时**完全以它为准**, `ClientWsDialReq_t.EnableTlsVerify` 被忽略。
- **生命周期归调用者**: 本库只用它, 不持有也不关闭它(`CloseForTest` 也不动)。建一个复用即可, 要回收时自己
  `CloseIdleConnections()`。不要在 `WsDialReqFn` 那种每次重连都会跑的地方造新实例, 会泄漏 transport。
- **不用担心 HTTP/2**: websocket 升级只能走 HTTP/1.1, 但 Go 的 `net/http` 已经内建处理了 —— 带
  `Connection: upgrade` + `Upgrade: websocket` 的请求会被 `Request.requiresHTTP1()` 标成 onlyH1,
  握手时清空 ALPN 并且不复用已缓存的 h2 连接。所以标准 `*http.Transport` 随便传(`ForceAttemptHTTP2`
  开着也没事)。只有塞进只会 h2 的自定义 `RoundTripper`(如 `x/net/http2.Transport`)才会连不上。

内网自研客户端连自研服务端时最实用的用法是**锁定服务端证书公钥**(只认公钥, 不看 CA / 有效期 / 域名),
完整可运行例子见 [`example/TlsPubKeyPin/`](example/TlsPubKeyPin/)。

## 工作模式: 通知 + 拉取

本框架的设计意图是作为"变更通知层", 配合 ajax 获取实际数据:

1. 服务端数据变更时 → `FireChange(roomId)`, 可选带少量 `LiveData`。
2. 客户端收到 onChange → 发 ajax 请求获取最新完整数据。
3. 断线重连 → 重新加入房间 → 发现版本号变了 → ajax 取最新数据。

这种模式的好处:

- 事件只存内存, 不需要数据库持久化事件, 没有事件存储/清理的复杂逻辑。
- 实际业务数据的持久化由已有的数据库负责, ws 层不重复存储。
- 性能容易优化: `FireChange` 只是内存操作 + 写 ws 缓冲, 不涉及 IO。
- 慢客户端不影响其他客户端: 写缓冲满了就断开那一个连接, 不阻塞广播。
- 不会爆内存: 每个连接独立的固定大小写缓冲(默认 64KB), `LiveData` 不存储。

对于 streaming 场景(如 AI streaming 输出)的经验:

- `LiveData` 1024 字节放一次 200ms 间隔内的增量文本, 大多数情况够用(200ms 内 AI 产生的文本通常几十到几百字节)。
- 偶尔一次增量超了 1024 字节, `LiveData` 会被丢弃, 但通知仍然发出, 前端发现 `LiveData` 为 null 时走 ajax 补取。
- 断线重连本身就要 ajax 重新取完整状态, 所以这个降级路径本来就得有。
- 不要试图通过 ws 推送完整的大块数据(如完整 streaming 内容), 否则会有阻塞/爆内存风险。
  让 ws 只负责通知, 大数据走 ajax, 两条路径各司其职。
- ⚠ 注意这里推荐的是**用 `LiveData` 装每次的增量文本**, 不是"只戳一下让客户端自己拉全文"。
  后者每次拉的都是**当前全文**, 累计流量 O(n²), 是吐字场景最容易踩的坑。
  详见 [`doc/webChatBestPractice.md`](doc/webChatBestPractice.md) 3.8 节。

为什么是"戳一下 + 拉取"而不是"直接用 ws 推内容当可靠", 以及为什么在本库约束下这已是已知最优结构(剩下只能调参数),
见 [`doc/whyNotifyNotPush.md`](doc/whyNotifyNotPush.md)。

### 适用场景

前提是 **ws 通知 + ajax(或 http rpc)取数** 两条路径配合: ws 只负责"戳一下"告诉客户端某个 room 变了,
真实数据和可靠性由业务自己的数据库 + ajax 负责。在这个前提下:

| 场景 | 是否适合 |
| --- | --- |
| 新消息提醒(告诉客户端"这个会话变了") | 适合 |
| 客户端收到通知后拉取最新消息列表 | 适合 |
| 未读数、会话列表刷新通知 | 适合 |
| typing / 在线状态这类当前状态量 | 适合(用 `CVersionId` 存当前状态, 进房/重连自动下发, 必要时 ajax 保底) |
| 每条聊天消息可靠送达 | 小规模适合(ws 通知 + ajax 兜底, 见 [`example/ReliableChat/`](example/ReliableChat/); 纯靠 ws `LiveData` 当可靠送达不适合)。**但正式做聊天软件的消息正文, 先看 [`doc/webChatBestPractice.md`](doc/webChatBestPractice.md)** —— 只戳不带内容会导致 ajax 泛滥 + 1.5 RTT 才看到消息, 那边给了六条判据和该用的协议 |
| 离线消息、消息历史、回放、断线补发 | 适合(历史存数据库 + ajax 拉取, ws 只负责戳一下, 见 [`example/ReliableChat/`](example/ReliableChat/)) |
| 大规模多节点分布式通知 | 需要额外改造(本库是单进程房间表)。看起来有办法解决、且全广播档不用改库, 理论分析见 [`doc/multiNodeDistribute.md`](doc/multiNodeDistribute.md)(**仅理论, 未实践**) |

### CVersionId 还是 LiveData

`FireChange` 可选携带 `CVersionId` 和 `LiveData`, 两者语义不同, 对接时容易选错:

| | 服务端存储 | 进房/重连 | 适合什么 |
| --- | --- | --- | --- |
| `CVersionId`(≤100 字节) | 存内存当前值(只存最新一个) | 进房/重连自动下发当前值 | **当前状态量**("是什么"): 在线/typing、未读数、数据版本号 |
| `LiveData`(默认 ≤1024 字节) | 不存, 仅实时传一次 | 重连/缓冲满/超限就没了 | **一次性增量**("发生了什么"): 新消息、streaming 文本片段 |

> ⚠ `CVersionId` 和 `LiveData` 一样**不保证总是有**(没带的 `FireChange` 会清空它、服务器重启会丢、进未变更过的房间为空, 且内容框架不解释、不保证单调可比)。
> 它是"省一次拉取"的机会主义提示, 不是可靠单调版本通道 —— 跳过全量的判据必须是"非空 + 可比 + 同 `RoomEpoch` + `<= 本地`", 其余一律回源拉全量。
> 把"`CVersionId <= 本地` 就直接跳过、连请求都不发"当唯一闸门会漏掉真实更新, 详见 [`doc/accelFieldsNotReliable.md`](doc/accelFieldsNotReliable.md)。

- **状态量用 `CVersionId`**。它是"当前完整状态", 丢一次通知没关系——下一次通知或进房/重连会带上最新值, **自动收敛到正确状态**。
  例如 typing / 在线状态: `FireChange(RoomId, CVersionId="在线状态编码")`, 客户端 `onChange` 直接读 `ev.CVersionId` 更新 UI, **不需要 ajax**;
  只在服务器重启(`CVersionId` 丢失、`RoomEpoch` 变化)时用 ajax 取一次当前完整状态保底。100 字节放状态编码或版本号通常够用, 不够就用 `CVersionId` 当版本号 + ajax 取完整列表。
- **一次性增量用 `LiveData`**。丢了(缓冲满/断线/超上限)就走 ajax 补。聊天消息属于这类(每条是新增内容, 不是"当前状态"), 见 [`example/ReliableChat/`](example/ReliableChat/)。

### 可靠送达 / 历史 / 断线补发

本框架可以做到"聊天消息可靠送达"和"离线消息/消息历史/回放/断线补发":把 ws 当"戳一下"的低延迟通道,
把 ajax(或 http rpc)当真实数据来源, 两者配合即可。对接代码很简单, 代价仅仅是某些情况(`LiveData` 不够用时)多一个 RTT。

核心思路(完整可运行代码 + 自动测试见 [`example/ReliableChat/`](example/ReliableChat/)):

1. 真正的可靠性锚点是**业务消息序号**(每个房间内单调递增, 存数据库), 不是 ws 层的 `ChangeSeq`
   (`ChangeSeq` 只是"戳一下"信号, 服务器重启会重置)。
2. 服务端每来一条消息: 先写库分配序号, 再 `FireChange`, 把整条消息塞进 `LiveData`(尽力而为)。
3. 客户端收到 onChange:
   - `LiveData` 有内容, 且序号正好接在本地最后一条之后 → 直接用 `LiveData` 应用, **0 额外 RTT**(快路径, 即"直接推送每一条聊天消息")。
   - 否则(`LiveData` 没有/被丢弃/超上限/重连/服务器重启, 或者序号跳号说明中间漏了)→ **ajax 拉取本地最后序号之后的全部消息**补齐。
4. "是不是断过线"不需要单独判断: 任何中断都会表现为"`LiveData` 缺失"或"序号跳号", 被上面的 ajax 路径统一兜住。
   这条 ajax 路径同时就是"离线消息/历史/回放/断线补发"的实现——新客户端进房、断线重连、服务器重启后, 都靠它把缺的消息补回来。

运行例子: `cd example && go run ./ReliableChat`; 跑自动测试: `cd example && go test ./ReliableChat`。
自动测试覆盖: 逐条快路径送达、突发连发不丢消息、超大 `LiveData` 降级 ajax、进房回放历史、ws 重启后断线补发。

## 客户端状态查询

客户端提供以下方法用于查询当前运行状态, 方便调试和 UI 展示。

Go 客户端:

```go
client.GetUiStatusToUser()          // 返回 "synced"/"syncing"/"offline"/"needManual"
client.GetSinceLastServerConfirm()  // 返回 time.Duration, 距离上次收到服务端有效消息的时间。从未收到过返回 -1。
client.GetNeedManualMsg()           // 返回 needManual 状态的原因文本。空字符串表示不在 needManual 状态。
client.GetRoomCount()               // 返回当前订阅的房间数量。
client.IsConnectedSucc()            // 返回 ws 是否已连接。
client.GetClientStatus()            // 返回 ClientStatus_t 结构体(含 Type/HasNeed/LastCloseReason/LastCloseLog/IsStopListen)。
```

TypeScript 客户端:

```ts
client.GetUiStatusToUser()          // 返回 "synced"/"syncing"/"offline"/"needManual"
client.GetSinceLastServerConfirm()  // 返回毫秒数。从未收到过返回 -1。
client.GetNeedManualMsg()           // 返回 needManual 状态的原因文本。空字符串表示不在 needManual 状态。
client.GetRoomCount()               // 返回当前订阅的房间数量。
client.IsConnectedSucc()            // 返回 ws 是否已连接。
```

前端调试面板示例:

```ts
const status = client.GetUiStatusToUser()
const sinceLast = client.GetSinceLastServerConfirm()
const manualMsg = client.GetNeedManualMsg()
debugDiv.textContent = `status=${status} lastConfirm=${sinceLast}ms rooms=${client.GetRoomCount()} needManual=${manualMsg}`
```

## 语义保证

本框架的保障是一条**活性**命题: **只要客户端与服务端最终都在线、且网络最终双向通畅并保持一段足够完成一次收敛的时间(此前允许任意长时间的断开、丢包、换网、服务器重启), 客户端最终一定收敛到房间的最新状态**(中间变更次数会丢失 / 塌缩)。这依赖两件**各自独立、缺一不可**的事——每次(重)连接做一次全量重新对齐, 以及独立主动探活(不能假设"TCP 没报错就等于连接正常", 中间盒丢包 / 客户端换 IP 都会造成无报错的"静默死链")。**自己实现 ws / SSE / pg `NOTIFY` 刷状态时同样必须做对这两件事**, 详见 [`doc/deliveryGuarantee.md`](doc/deliveryGuarantee.md)(含检查清单)。

保证:

- 在网络正常时, 客户端最终一定能感知到房间"是否发生了变化"(通过 `RoomEpoch`+`ChangeSeq` 去重)。
  即: 如果服务端 `FireChange` 了, 客户端一定会收到一次 onChange 回调(去重后);
  如果服务端没有 `FireChange`, 客户端不会收到虚假的 onChange 回调。
- 断线重连后, 客户端重新加入房间会收到当前版本号, 如果和断线前不同则触发 onChange。
  所以客户端不会错过"最终状态有变化"这件事。

不保证:

- 中间状态可能丢失。比如服务端连续 `FireChange` 了 3 次(v1→v2→v3), 客户端可能只收到 v3。
  断线重连期间的所有中间变更都会丢失, 客户端只看到重连后的最新版本。客户端处理太慢, 中间变更也可能会丢失。
- `LiveData` 不保证送达。服务端写缓冲满时丢弃, 断线时丢失, 超过上限时丢弃。
  `LiveData` 是尽力而为的附加数据, 不是可靠传输。
  注意: "不可靠"不等于"无用/该删"——它是可选的延迟优化(命中省一个 RTT + 避免 ajax 惊群),
  不传时本库就是纯变化触发器, 不付任何代价。把它误当"可靠增量重放流"才是错的, 详见
  [`doc/accelFieldsNotReliable.md`](doc/accelFieldsNotReliable.md)。
- `CVersionId` 不保证总是有, 也不保证单调可比。没带 `CVersionId` 的 `FireChange` 会把它清空,
  服务器重启会丢失, 进未变更过的房间下发为空; 框架不解释其内容、不保证它是数字或递增。
  它是和 `LiveData` 同级的尽力而为提示, 只能当"省一次拉取"的机会主义优化,
  不能当跳过全量的唯一判据(否则会漏掉真实更新)。详见 [`doc/accelFieldsNotReliable.md`](doc/accelFieldsNotReliable.md)。

不实现:

- 不实现注册订阅模式(即不存储事件历史, 不支持从某个版本号开始回放)。
  客户端的职责是: 收到变更通知后, 自己通过 ajax 去获取最新完整数据。
  本框架只负责"戳一下"告诉客户端该去取了, 不负责传输完整业务数据。

## 限制

- `LiveData` 默认最大 1024 字节, 可由 `ServerManager.LiveDataMaxSize` 调大(最大 16MB)。超过该上限的数据静默丢弃(不发送 `LiveData` 但通知仍然发出)。
  - `LiveDataMaxSize` 必须 ≤ `WriteBufMaxBytes`(每连接写缓冲, 默认 64KB, 最大 64MB)的 25%, 否则初始化时 panic。
  - `LiveDataMaxSize`/`WriteBufMaxBytes`/`RoomEnterMaxPerConn` 必须在首次调用 API 前配置好, 之后不可更改(无锁读取, `_init` 把默认值写回字段本身)。这三个字段 `0` 表示用默认值, **负值视为调用者 bug, `_init` 时直接 panic**(不会被静默当成默认值)。
  - 超过单 frame(约 64KB)的 `LiveData` 自动用 `roomValueMore`...`roomValue` 分块传输。**调大 `WriteBufMaxBytes` 时, 客户端的 `ReadMsgMaxBytes` 必须 ≥ 服务端 `WriteBufMaxBytes`**(单个 websocket message 最大可达该值), 否则客户端会因消息过大断开。默认值(两端 64KB)下行为与旧版完全一致。
  - 注意: `LiveData` 越大, 越偏离"通知"定位, 热房间 fanout 下每条连接各缓存一份, 内存放大明显。大体积仅适合连接数少、低频的场景, 默认 1024 已覆盖绝大多数"一次性增量"需求。
- `CVersionId` 最大 100 字节。超过会 panic。
- `RoomId` 最大 1024 字节(服务端校验)。超过会断开连接。
- 服务端单条协议消息(序列化后)最大 65535 字节(下层 frame 上限)。更大的 `LiveData` 通过分块跨多条消息传输。
- 服务端不存储 `LiveData`, 客户端断线重连后不会补发之前的 `LiveData`。
- 房间没有历史记录, 客户端只能收到订阅后的变更。

## 配置参数

本 package 对外可配置的全部参数(`ServerManager` / `Client` / `TimeoutCfg_t`)及其默认值、上限、效果,
见 [`doc/config.md`](doc/config.md)。

## 协议

- 二进制协议, 一个 WebSocket message 可以包含多个协议消息。
- 每个协议消息格式: `[uint16LE 长度][消息体]`。
- 消息类型: `ping(1)`, `setTimeCfg(2)`, `roomEnter(3)`, `roomLeave(4)`, `roomValue(5)`, `identity(6)`, `connAllow(7)`, `deny(8)`, `closeConn(9)`, `roomValueMore(10)`。
  - `roomValueMore`: 服务端→客户端。`LiveData` 超过单 frame 上限时, 一次 `roomValue` 拆成 `[roomValueMore...][roomValue]` 分块传输(More=后面还有, 最后一片是普通 `roomValue` 携带元数据, 与不分块时同构)。同连接上整组分片连续到达、中间不插其它消息, 客户端把累积的 More 分片拼到最后那条 `roomValue` 前面重组。
  - `identity`: 客户端→服务端首包(opaque 凭证)。
  - `connAllow`: 服务端→客户端连接已批准(携带 `AuthEnabled`)。
  - `deny`: 服务端→客户端拒绝(连接/房间)。
  - `closeConn`: 服务端→客户端要求关闭(临时/永久)。
- 认证流程: 客户端连上先发 `identity`, 服务端配置了 `OnAllowFn` 则等批准(`connAllow`)再发 `roomEnter`; 未配置则服务端立即发 `connAllow`(`AuthEnabled=false`), 零额外往返。
- `setTimeCfg` 使用 KV 格式: `[count: uint8][ [fieldId: uint8][value: int64LE] ] * count`。`fieldId` 见 `TimeoutCfg_t` 注释, 不认识的 `fieldId` 跳过(前向兼容)。
- 心跳: 客户端空闲时主动发 ping, 服务端回复 ping。超时未收到数据则断线重连。
- 服务端写缓冲满时主动断开该连接(客户端太慢, 丢弃)。

## 示例

所有例子在 [`example/`](example/) 目录下, 是一个**独立的 go module**(自己的 `go.mod`),
这样浏览器真机测试用到的 chromedp 等测试依赖不会泄漏进本库(`hgmRoomNotify`)的 `go.mod`。
例子通过 `replace` 指向同仓库本体源码, 始终对着当前 commit 编译。运行前先 `cd hgmRoomNotify/example`。

| 例子 | 说明 | 运行 / 测试 |
| --- | --- | --- |
| [`SimpleDemo/`](example/SimpleDemo/) | 最小闭环: 同进程起服务端 + Go 客户端, `FireChange` 通知。 | `go run ./SimpleDemo` / `go test ./SimpleDemo` |
| [`ReliableChat/`](example/ReliableChat/) | ws 戳一下 + ajax 兜底实现可靠聊天: 可靠送达 / 离线消息 / 历史回放 / 断线补发(纯 Go)。 | `go run ./ReliableChat` / `go test ./ReliableChat` |
| [`SoftwareUpdate/`](example/SoftwareUpdate/) | 软件自动更新对接: 启动先 check api(可靠数据源), 已最新再用 ws + `CVersionId`(加速字段)实时下发新版本。演示 `CVersionId` 正确用法(纯 Go)。 | `go run ./SoftwareUpdate` / `go test ./SoftwareUpdate` |
| [`TlsPubKeyPin/`](example/TlsPubKeyPin/) | 用 `Client.HttpClient` 锁定服务端证书的**公钥**(只认公钥, 不看 CA / 有效期 / 域名)。含"标准验证失败 / 锁对公钥连上 / 锁错公钥连不上"三场景对比与真 tls 自动测试。 | `go run ./TlsPubKeyPin` / `go test ./TlsPubKeyPin` |
| [`WebChat/`](example/WebChat/) | 浏览器 **React** 前端 + Go 后端(内存库)的可靠聊天室, 复用 ReliableChat 的可靠模式; 浏览器客户端编译前复制进前端(gitignore, 仓库不留第二份)。含真 Chrome 真机自动测试。 | `go run ./WebChat/WebChatRun`(一条命令自动编译前端+起后端) / `go test ./WebChat` |

## 设计文档

`doc/` 目录下的设计与分析记录:

| 文档 | 内容 |
| --- | --- |
| [`doc/pushCorrectness.md`](doc/pushCorrectness.md) | **判断"我这么做对不对"从这篇开始**: 推送正确性的三条规则(必须有全量对齐这条路 / 必须能触发它 / 通知窗口必须覆盖快照点), 逐环逐流套用的检查方法, 以及十种真实存在的推送形式(手动刷新 / 轮询 / 长轮询 / 纯戳 / 戳+版本号 / 推全量 / 推增量+序号 / CRDT / 日志跟随 / APNs 唤醒)在 **正确性 / 实时性 / 性能** 三个指标上的分类分析。含终极断言与故障注入清单。 |
| [`doc/webChatBestPractice.md`](doc/webChatBestPractice.md) | **什么时候不该用本库**: "戳一下 + 拉取" 与 "推内容 + 序号" 的六条选择判据(塌缩率 / 变更描述与内容之比 / 延迟 / 扇出同步尖峰 / 移动耗电 / UI 即时性), 以及聊天正文该用的完整 ws 协议设计。含纯戳的正面案例(Figma LiveGraph / IMAP IDLE)与它成立的前提条件。 |
| [`doc/pushDesignAndReview.md`](doc/pushDesignAndReview.md) | **执行手册: 按 正确性 > 延时 > 并发 > 带宽 > 简单性 做一套 ws/tcp 推送, 具体怎么执行、做完怎么检查。** 七步走(写可证伪的目标 → 把数据切成桶逐桶选方案 → 写协议帧 → 写服务端 → 写客户端 → 参数表与三条不变式 → 搭测试过 22 条验收清单), 每条规则带一行"违反后果"。**单独看这一篇就能执行完整套**, 不需要先读别的文档。 |
| [`doc/pushDesignRationale.md`](doc/pushDesignRationale.md) | 上一篇的**由来**: 每条规则为什么是那样。核心是"**并发目标决定了版本号必须内生于数据**"(锁不是为了保护数据, 是为了给外生号赋予意义), 以及由此穷举出的三种 id 数据结构、seqlock 不变式推导、"订阅先于快照 + 缓冲重放"等价于锁内原子的证明、帧信封与压缩的实测账、以及踩过的坑。**想删掉/放宽执行手册里某条规则时才需要读。** |
| [`doc/deliveryGuarantee.md`](doc/deliveryGuarantee.md) | 最终收敛保障的精确目标, 以及任何同类系统(含 pg `NOTIFY` / 手写 SSE)都必须做对的两件正交的事: 每次(重)连接全量重新对齐 + 独立主动探活(应对中间盒丢包 / 换 IP 造成的无报错静默死链)。含通知级联时"短板决定整条链、hgmRoomNotify 补不回上游丢的"分析与自查清单。 |
| [`doc/config.md`](doc/config.md) | 全部可配置参数(`ServerManager` / `Client` / `TimeoutCfg_t`)的默认值、上限与效果。 |
| [`doc/whyNotifyNotPush.md`](doc/whyNotifyNotPush.md) | 为什么用"ws 戳一下 + DB 拉取"而非"ws 直推内容当可靠", 以及与 Kafka 等方案的对比。 |
| [`doc/accelFieldsNotReliable.md`](doc/accelFieldsNotReliable.md) | `LiveData` 与 `CVersionId` 是同一类**加速字段**(命中省一次回源, 没命中就回源), 不是可靠字段。回应两个对称误区: "`LiveData` 不可靠、该删" 与 "`CVersionId` 是可靠单调版本号、`<= 本地` 就能跳过全量"。 |
| [`doc/hotRoomFanout.md`](doc/hotRoomFanout.md) | 热房间扇出成本与合并缓冲分析(当前有意不实现合并缓冲的原因)。 |
| [`doc/multiNodeDistribute.md`](doc/multiNodeDistribute.md) | 多节点分布式通知的理论分析(**仅理论, 未实践**)。 |

## License

[Unlicense](https://unlicense.org/)(public domain)
