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

`CVersionId` 与 `LiveData` 的选型(状态量 vs 一次性增量)见主 README 的「CVersionId 还是 LiveData?」一节。
