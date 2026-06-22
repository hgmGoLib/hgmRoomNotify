本模块的文档见 README.md(Markdown 格式)。

设计文档在 doc/ 目录下:
  doc/config.md                  全部可配置参数的默认值、上限与效果。
  doc/whyNotifyNotPush.md        为什么用"ws 戳一下 + DB 拉取"而非"ws 直推内容当可靠"。
  doc/liveDataNotReplayStream.md LiveData 是机会主义快路径而非可靠重放流(问答形式)。
  doc/hotRoomFanout.md           热房间扇出成本与合并缓冲分析。
  doc/multiNodeDistribute.md     多节点分布式通知的理论分析(仅理论, 未实践)。
