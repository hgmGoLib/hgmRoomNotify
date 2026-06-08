# ReliableChat 例子

用 hgmRoomNotify(ws "戳一下") + ajax(取真实数据)实现:

- **每条聊天消息可靠送达**
- **离线消息 / 消息历史 / 回放 / 断线补发**

对接代码很简单, 代价仅仅是某些情况(LiveData 不够用时)多一个 RTT。

运行:

```
go run ./example/ReliableChat      # 跑 demo
go test ./example/ReliableChat/    # 跑自动测试
```

## 一句话原理

ws 只负责"戳一下"告诉客户端"有变化了", **真实消息数据和可靠性都靠业务自己的数据库 + ajax**。
ws 的 `LiveData` 是一条**尽力而为的快路径**(能省一个 RTT), 兜底永远是 ajax。

真正的可靠性锚点是**业务消息序号 `MsgIndex`**(每个房间内单调递增, 存数据库),
**不是** ws 层的 `ChangeSeq`(`ChangeSeq` 服务器重启会重置, 只是"戳一下"信号)。

## 这些问题分别是怎么解决的

### 1. 直接推送每一条聊天消息, 并当作"可靠送达"

| 步骤 | 做法 | 代码 |
| --- | --- | --- |
| 服务端发消息 | 先写库分配 `MsgIndex`, 再 `FireChange` 把整条消息塞进 `LiveData` | `chat_server.go: Post` |
| 客户端收到 onChange(快路径) | `LiveData` 有, 且序号正好接在本地最后一条之后 → 直接应用, **0 额外 RTT** | `chat_client.go: onChange` |
| 客户端收到 onChange(兜底) | `LiveData` 没有 / 被丢弃 / 序号跳号 → ajax 拉本地最后序号之后的全部 | `chat_client.go: backfillByAjax` |

"可靠"来自兜底: 只要 `FireChange` 了, 库保证客户端最终一定收到一次 onChange, 客户端要么快路径拿到, 要么 ajax 补到。**一条都不会丢**。
对应测试: `TestReliableChat_FastPath`(逐条快路径)、`TestReliableChat_BurstNoLoss`(连发被合并也不丢)。

### 2. LiveData 被丢弃(超 1024 字节 / 写缓冲满)

客户端发现 `LiveData` 为空 → 直接走 ajax 兜底, 不丢消息。
对应测试: `TestReliableChat_OversizeLiveData`(2000 字节消息, `LiveData` 被库丢弃, 经 ajax 补到)。

### 3. 离线消息 / 消息历史 / 回放

消息历史存在数据库里(`chatStore`)。新客户端上线进房时, 进房本身会触发一次 onChange,
客户端发现本地序号落后 → ajax 拉全部历史。
对应测试: `TestReliableChat_HistoryReplayOnJoin`(房间已有 5 条历史, 新客户端进房自动回放)。

### 4. 断线补发 / 服务器重启

断线重连后客户端重新进房, 服务端下发的 `RoomEpoch`/`ChangeSeq` 与本地不一致 → 触发 onChange →
ajax 拉断线期间漏掉的消息。服务器(ws 层)重启只是换了 `RoomEpoch`, 数据库还在, 一样补得回来。
对应测试: `TestReliableChat_ReconnectBackfill`(ws 层重启 + 期间产生离线消息, 重连后补齐, 之后实时消息恢复快路径)。

### 为什么不用单独判断"是不是断过线"

任何中断(断线、丢包、缓冲满、超 1024、服务器重启)都会表现为**"`LiveData` 缺失"或"序号跳号"**,
被同一条 ajax 兜底路径统一处理。所以客户端逻辑只有"快路径 / ajax 兜底"两个分支, 不需要显式的断线检测。

## 修正后的适用场景表

原始 review 把下面两行判为"不适合", 那是**假设只用 ws 这一条通道**。
本库的设计意图本来就是 **ws 通知 + ajax 取数**两条路径配合, 在这个前提下应改为:

| 场景 | 是否适合 |
| --- | --- |
| 新消息提醒:告诉客户端"这个会话变了" | 适合 |
| 客户端收到通知后拉取最新消息列表 | 适合 |
| 未读数、会话列表刷新通知 | 适合 |
| typing / 在线状态这类当前状态量 | 适合(用 **`CVersionId`** 存当前状态, 进房/重连自动下发, 见下方说明; 必要时 ajax 保底) |
| 直接推送每一条聊天消息并当作可靠送达 | **适合**(ws 通知 + ajax 兜底, 见本例;**纯靠 ws `LiveData` 当可靠仍不适合**) |
| 离线消息、消息历史、回放、断线补发 | **适合**(历史存数据库、ajax 拉取, ws 只负责戳一下, 见本例) |
| 大规模多节点分布式聊天通知 | 需要额外改造(本库是单进程房间表, 多节点需自己做 `FireChange` 的跨节点广播) |

关键区别: **"纯靠 ws 推送当可靠" 不适合**(`LiveData` 是尽力而为, 会丢);
**"ws 戳一下 + ajax 兜底" 适合**, 代价是 `LiveData` 不够用时多一个 RTT。

## 该用 CVersionId 还是 LiveData?

这是对接时最容易选错的一点(本例的聊天消息用的是 `LiveData`, 但很多场景应该用 `CVersionId`):

| | 服务端存储 | 进房/重连 | 适合什么 |
| --- | --- | --- | --- |
| `CVersionId`(≤100 字节) | **存内存当前值** | **进房/重连自动下发当前值** | **当前状态量**("是什么"): 在线/typing、未读数、数据版本号 |
| `LiveData`(≤1024 字节) | **不存, 仅实时传一次** | 重连/缓冲满/超限就没了 | **一次性增量**("发生了什么"): 这条新消息、streaming 文本片段 |

- **状态量用 `CVersionId`**。它是"当前完整状态", 所以丢一次通知没关系——下一次通知或进房/重连会带上最新值, **自动收敛到正确状态**。
  例如 typing / 在线状态: `FireChange(RoomId, CVersionId="在线状态编码")`, 客户端 `onChange` 直接读 `ev.CVersionId` 更新 UI, **不需要 ajax**;
  只在服务器重启(`CVersionId` 丢失、`RoomEpoch` 变化)时, 用 ajax 取一次当前完整状态保底。100 字节放状态编码或一个版本号通常够用, 不够就用 `CVersionId` 当版本号 + ajax 取完整列表。
- **一次性增量用 `LiveData`**。丢了(缓冲满/断线/超 1024)就走 ajax 补。本例的聊天消息属于这类: 每条消息是新增内容, 不是"当前状态", 所以用 `LiveData` 快路径 + ajax 兜底。

## 大规模多节点分布式怎么改造

本库是**单进程房间表**: `FireChange` 只遍历本进程内存里该 room 的连接。多节点要补的核心是**跨节点扇出**, 不是简单按用户分片:

- 通知是按 **room** 维度的, 而连接按**用户**分布 → **同一个 room 的订阅者天然散落在多个节点**。
  所以 `FireChange(room)` 不能只在本节点发, 必须送到所有持有该 room 订阅者的节点。
- **"按用户 id 分片" 只是连接层的会话保持/负载均衡**(用户落到哪个 ws 节点), 这层可以做, 但替代不了通知层的扇出。
- 正确形态: 把 `FireChange` 从"本进程内存广播"改成 **发布到 pub/sub 总线**(Redis / NATS / Kafka)。
  所有 ws 节点订阅总线, 收到后各自对本地连接 `FireChange`。这就是"`FireChange` 变 rpc"的本意——**fan-out, 不是点对点 rpc**。
- 起步用**全广播**即可: 本库通知很小(只戳一下, 真实数据走 ajax), 广播成本低。
  规模再大时维护 `room → 节点` 路由表(各节点上报自己持有哪些 room 的订阅), `FireChange` 只发相关节点, 减少广播放大。
- 别忘了 `CloseConnBySessionId`(踢人/撤权)同样要跨节点: 用户连在哪个节点不确定, 也走总线广播一个 "close sessionId" 事件。

也就是说: 你设想的"多开几个服务器 + `FireChange` 变 rpc + 负载均衡"方向是对的,
只要把 `FireChange` 理解为"发布到消息总线做跨节点扇出"(而不是按用户分片就够了), 就能水平扩展。
