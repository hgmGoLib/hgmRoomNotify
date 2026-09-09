本模块的文档见 README.md(Markdown 格式)。

设计文档在 doc/ 目录下:
  doc/pushCorrectness.md         推送正确性的三条规则 + 十种常见形式在 正确性/实时性/性能 上的分类分析。
                                 判断"我这么做对不对"从这篇开始。
  doc/webChatBestPractice.md     什么时候不该用本库: "戳一下+拉取" 与 "推内容+序号" 的六条选择判据,
                                 以及聊天正文该用的完整 ws 协议设计。
  doc/pushDesignAndReview.md     执行手册: 按 正确性>延时>并发>带宽>简单性 做一套 ws/tcp 推送,
                                 具体怎么执行 + 做完怎么检查。七步走, 每条规则带一行"违反后果",
                                 最后是 22 条验收清单。单独看这一篇就能执行完整套。
  doc/pushDesignRationale.md     上一篇的由来: 每条规则为什么是那样。核心是"并发目标决定了版本号
                                 必须内生于数据", 以及三种 id 数据结构的穷举、各条推导与踩过的坑。
                                 想删掉/放宽执行手册里某条规则时才需要读。
  doc/deliveryGuarantee.md       最终收敛保障目标 + 8 条正确性自查清单(传输模型无关, 自己撸也要照做)。
  doc/config.md                  全部可配置参数的默认值、上限与效果。
  doc/whyNotifyNotPush.md        为什么用"ws 戳一下 + DB 拉取"而非"ws 直推内容当可靠"。
  doc/accelFieldsNotReliable.md  LiveData 等加速字段是机会主义快路径而非可靠重放流(问答形式)。
  doc/hotRoomFanout.md           热房间扇出成本与合并缓冲分析。
  doc/pingTime.md                心跳间隔与读超时的参数选取。
  doc/multiNodeDistribute.md     多节点分布式通知的理论分析(仅理论, 未实践)。
  doc/数据量大可能出现的问题.md  数据量变大后可能撞上的 6 类问题清单(未遇到, 刻意都不做)。
