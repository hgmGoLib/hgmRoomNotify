# WebChat 例子（浏览器 React + golang 后端）

一个能跑起来的可靠聊天室：**React 前端 + golang 后端（数据库就是后端内存）**，
演示浏览器侧怎么用 `hgmRoomNotify` 对接。可靠传输沿用 [`../ReliableChat`](../ReliableChat) 的模式
（ws 只“戳一下”，真实数据和可靠性靠业务消息序号 `MsgIndex` + ajax 兜底），只是这里在浏览器侧
用 TypeScript + React 重新实现了一遍对接逻辑。

`hgmRoomNotify` 的浏览器客户端不在仓库里留第二份：**编译前**由 `npm` 的 prebuild 钩子
（`web/scripts/copy-client.mjs`）把 `hgmRoomNotifyBrowserTs/` 覆盖复制进 `web/src/hgmRoomNotifyBrowserTs/`，
应用源码用本地路径 `import "./hgmRoomNotifyBrowserTs/index.ts"`（不用 `../` 爬出 web 工程）。
这份复制品已 gitignore，所以仓库里始终只有一份 TS 源码。

## 怎么启动

一条命令搞定（编译前端 + 起后端都在里面）：

```bash
cd hgmRoomNotify/example
go run ./WebChat/WebChatRun
# 浏览器打开 http://127.0.0.1:8080 , 多开几个标签页互相发消息即可。
```

`WebChatRun/main.go` 先用一个 `.go` 文件向上定位到 `hgmRoomNotify` 开源项目根
（锚是 `go.mod` 里的 `module github.com/hgmGoLib/hgmRoomNotify` 行，不写死 `../` 层数，子树怎么挪都不怕），
再相对项目根做编译逻辑：缺依赖先 `npm install`，再 `npm run build`（prebuild 钩子复制客户端 TS + esbuild
打包 React 到 `web/dist/app.js`），最后起后端把内嵌好前端 JS 的页面发出去。在哪个目录敲命令都行。

只改前端、想要热打包，可在 `web/` 下跑 `npm run dev`（esbuild watch），后端不用重启（页面刷新即可）。

## 后端做了什么

| 路由 | 作用 | 代码 |
| --- | --- | --- |
| `/ws` | `hgmRoomNotify` 的通知通道（戳一下） | `chat_server.go: Routes` |
| `/chat/post` | 浏览器发消息（POST）：写内存库分配 `MsgIndex` + `FireChange` | `chat_server.go: handlePost` |
| `/chat/after` | ajax 取真实数据：返回 `MsgIndex > after` 的全部消息 | `chat_server.go: handleAfter` |
| `/` | 返回内嵌前端 JS 的页面，并种 `chatSession` cookie（断线重连用） | `chat_server.go: Routes` |

数据库就是 `chat_store.go` 里的纯内存 `ChatStore_t`，进程重启即清空。

## 前端做了什么（可靠对接的核心）

`web/src/main.tsx` 的 `onChange` 回调：

- **快路径**：`ev.LiveData` 带着整条消息且正好是 `lastIndex+1` → 直接渲染，0 额外 RTT。
- **兜底**：`LiveData` 缺失 / 被丢弃 / 跳号（连发被合并、断线、服务器重启）→ ajax 拉
  `lastIndex` 之后的全部补齐。ajax 失败时在回调里设 `ev.ErrMsg` → 客户端进入 `needManual`
  状态，页面顶部弹出报错提示用户刷新。

UI 顶部的“状态”来自 `client.GetUiStatusToUser()`（`synced/syncing/offline/needManual`）。

## 真机自动测试

```bash
cd hgmRoomNotify/example
go test ./WebChat          # 无需先 npm build，测试内部会自动打包
```

测试（`webchat_test.go`）和一键入口复用同一套定位+编译逻辑（`repo_build.go` 的 `FindHgmRoomNotifyRoot` / `BuildFrontend`）：
定位项目根 → `npm run build`（自动复制客户端 + esbuild 打包）→ 起真后端 → 用真 Chrome（`zlib_test.go` 里基于
`github.com/chromedp/chromedp` 的简化封装，不依赖私有库）加载页面，
验证正常路径：浏览器连上进房间、收到服务端推的消息、在页面里真实输入并点发送、渲染结果与后端内存库一致。
（首次运行若 `web/node_modules` 不存在会自动 `npm install`。异常路径本次不测。）
