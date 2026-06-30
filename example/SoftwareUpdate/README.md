# SoftwareUpdate 示例: 软件自动更新对接

用 `hgmRoomNotify` 做"软件自动更新"的最小可运行示例。它演示 [`doc/accelFieldsNotReliable.md`](../../doc/accelFieldsNotReliable.md)
里讲的 `CVersionId` **正确用法**: `CVersionId` 是加速字段(实时戳一下省去轮询), 不是可靠版本通道, 最终一律以
check api 回源为准。

## 设计要点: 正确性靠 check api, ws 只加速

**软件更新坏了很恶心, 所以正确性绝不押在不可靠的 ws 上。** 本例把两件事彻底分开:

- **保底(正确性)= check api, 完全不依赖 ws**:
  - 进程启动 **10 秒**后**无条件**调一次 `GET /update/check?version=<当前版本>` —— 哪怕 ws 被封 / 连不上, 这一步照跑。
  - 之后每隔一段(生产可设几小时)再周期 check 一次。
  - 只要进程在跑, 即使 ws 全程不通, 也能靠这两步把更新拿到(顶多晚一个周期)。
- **加速(实时性)= ws + `CVersionId`**: 服务端一发布就 `FireChange` 戳一下, 让客户端不必等到下个 check 周期就回源。
  它命不命中、丢不丢、`CVersionId` 有没有, **都不影响最终能否更新**, 只影响"多快发现"。

自动测试 `TestWsBlockedStillUpdatesViaPoll` 专门证明: ws 全程连不上时, 客户端仍靠周期 check 正确拿到新版本。

## 流程

1. 客户端进程启动 **10 秒**后, 调一次 check api(可靠数据源, 不碰 ws), 服务端比较版本返回 `{NeedUpdate, LatestVersion, DownloadURL}`。
2. 若需要更新 → 拿到下一个版本的下载地址(真实场景: 下载并重启到新版本)。
3. 若已是最新 → 进 `appUpdate:stable` 房间用 ws 实时监听(加速), 同时起一个周期 check(保底)。
4. 服务端发布新版本时 `FireChange(RoomId, CVersionId=新版本号)` —— 把最新版本号放进 `CVersionId` 实时戳给在线客户端,
   客户端无需等到下个周期即可回源 check api 拿权威结果和下载地址。ws 没通时, 这一步缺席, 但周期 check 仍会兜住。

## CVersionId 在这里的角色: 加速, 不是依据

`onChange` 收到 `CVersionId`(当前最新版本号)时:

| 情况 | 动作 |
|---|---|
| `CVersionId` 非空 **且** 可比 **且** 同 `RoomEpoch` **且** `<= 我当前版本` | 跳过回源(重连回放/重复戳, 我已是最新) |
| 其它一切(空 / 不可比 / `RoomEpoch` 变 / 比我新) | 回源 `GET /update/check` 拿权威结果 |

为什么不能"`CVersionId` 一比就拍板, 不回源": `CVersionId` 不保证总是有 —— 没带的 `FireChange` 会把它清空、
服务器重启会丢、进未发布过的房间为空。所以**只有它明确"不比我新"时才安全跳过**, 任何缺失/不可比/纪元变都必须回源。
否则一次带着空 `CVersionId` 的真实发布就会被漏掉。下载地址等权威信息也只在 check api 里, `CVersionId` 只够"提个醒"。

## 运行 / 测试

```
cd hgmRoomNotify/example
go run ./SoftwareUpdate     # 演示: 启动检查(已最新)-> ws 监听 -> 服务端发布 1.1.0 -> 客户端实时拿到更新
go test ./SoftwareUpdate    # 自动测试(真 tcp+http+ws, 无 mock)
```

自动测试覆盖: 启动已最新转监听、启动即需更新、ws 实时下发更新、`CVersionId<=本地`加速跳过、空 `CVersionId` 强制回源、
**ws 被封时靠周期 check 仍正确拿到更新**。

文件:

- `update_server.go` —— check api(可靠数据源) + 发布新版本(`Publish` 把版本号放进 `CVersionId`)。
- `update_client.go` —— 启动延迟 check + 周期保底 check(不依赖 ws) + 进房监听 + `onChange` 里 `CVersionId` 的正确取舍。
- `version.go` —— 点分版本号比较(比不出来返回 `ok=false`, 调用方据此回源)。
- `update_test.go` —— 真实环境自动测试。
