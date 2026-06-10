/*
 * WebChat 前端(React). 演示浏览器如何用 hgmRoomNotify 做可靠聊天室.
 *
 * 可靠传输沿用 example/ReliableChat 的模式(只是这里用 TypeScript + React 重新实现一遍对接逻辑):
 *   - ws 只"戳一下": onChange 回调表示"房间有变化了".
 *   - 快路径: ev.LiveData 带着整条消息, 且正好接在本地 lastIndex 之后 -> 直接应用, 0 额外 RTT.
 *   - 兜底: LiveData 缺失/被丢弃/跳号(连发被合并/断线/服务器重启) -> ajax 拉 lastIndex 之后的全部补齐.
 *   - 可靠性锚点是业务消息序号 MsgIndex(房间内单调递增), 不是 ws 层的 ChangeSeq.
 *
 * hgmRoomNotify 浏览器客户端是"引用"而非复制: 直接从源码相对路径 import, 由 esbuild 打包进来.
 */
import {useEffect, useRef, useState} from "react"
import {createRoot} from "react-dom/client"
// hgmRoomNotifyBrowserTs 在编译前由 scripts/copy-client.mjs 覆盖复制进本地(已 gitignore), 故用本地路径 import.
import {hgmRn_Client} from "./hgmRoomNotifyBrowserTs/index.ts"
import type {hgmRn_RoomOnChange_t} from "./hgmRoomNotifyBrowserTs/index.ts"

// 一条聊天消息. 字段名与后端 ChatMsg_t / JSON 字段名 ascii 一致.
interface ChatMsg_t {
    RoomId: string
    MsgIndex: number
    Sender: string
    Text: string
}

interface WebChatCfg_t {
    roomId: string
    sender: string
}

function getCfg(): WebChatCfg_t {
    const c = (window as any).__webchatCfg || {}
    const roomId: string = c.roomId || "chat:room1"
    // 没指定用户名时随机生成一个, 方便多标签页区分.
    const sender: string = c.sender || ("user-" + Math.floor(Math.random() * 9000 + 1000))
    return {roomId, sender}
}

// 由当前页面地址推导 ws 连接地址(同源).
function wsUrlFromLocation(): string {
    const proto = location.protocol === "https:" ? "wss://" : "ws://"
    return proto + location.host + "/ws"
}

function App() {
    const cfg = useRef(getCfg()).current
    const [messages, setMessages] = useState<ChatMsg_t[]>([])
    const [status, setStatus] = useState("syncing")
    const [errMsg, setErrMsg] = useState("")
    const [draft, setDraft] = useState("")
    const [sending, setSending] = useState(false)

    // 可靠性核心状态: 已连续应用到的消息 + 最大 MsgIndex. 放 ref 里给 onChange 闭包读写.
    const appliedRef = useRef<ChatMsg_t[]>([])
    const lastIndexRef = useRef(0)
    const clientRef = useRef<hgmRn_Client | null>(null)

    // 把当前 applied 同步到 React 状态触发渲染.
    function flush() {
        setMessages(appliedRef.current.slice())
    }

    // ajax 把 lastIndex 之后的消息全部拉回来补齐(兜底/历史/断线补发的统一入口).
    async function backfillByAjax(ev: hgmRn_RoomOnChange_t) {
        const after = lastIndexRef.current
        let list: ChatMsg_t[]
        try {
            const resp = await fetch("/chat/after?roomId=" + encodeURIComponent(cfg.roomId) + "&after=" + after)
            if (!resp.ok) {
                throw new Error("status " + resp.status)
            }
            list = await resp.json()
        } catch (e) {
            // 在 onChange 回调里设置 ErrMsg -> 客户端进入 needManual 状态, UI 提示用户手动刷新.
            ev.ErrMsg = "ajax 补取失败: " + String(e)
            setErrMsg(ev.ErrMsg)
            return
        }
        for (const m of list) {
            if (m.MsgIndex === lastIndexRef.current + 1) {
                appliedRef.current.push(m)
                lastIndexRef.current = m.MsgIndex
            }
        }
        flush()
    }

    function onChange(ev: hgmRn_RoomOnChange_t) {
        // 快路径: LiveData 带着整条消息, 且正好接在 lastIndex 后面 -> 直接应用.
        if (ev.LiveData && ev.LiveData.length > 0) {
            try {
                const m: ChatMsg_t = JSON.parse(new TextDecoder().decode(ev.LiveData))
                if (m.RoomId === cfg.roomId) {
                    if (m.MsgIndex <= lastIndexRef.current) {
                        return // 重复/旧消息, 忽略.
                    }
                    if (m.MsgIndex === lastIndexRef.current + 1) {
                        appliedRef.current.push(m)
                        lastIndexRef.current = m.MsgIndex
                        flush()
                        return
                    }
                    // 跳号: 落到下面 ajax 补齐.
                }
            } catch {
                // 解析失败也走 ajax 兜底.
            }
        }
        void backfillByAjax(ev)
    }

    useEffect(() => {
        const client = new hgmRn_Client()
        clientRef.current = client
        client.setUrl(wsUrlFromLocation())
        const leaveFn = client.roomEnter(cfg.roomId, onChange)
        // 暴露给真机自动测试(调试用): 读连接状态.
        ;(window as any).__webchatClient = client

        // 定时刷新面向用户的 ui 状态(synced/syncing/offline/needManual).
        const timer = window.setInterval(() => {
            setStatus(client.GetUiStatusToUser())
            const nm = client.GetNeedManualMsg()
            if (nm !== "") {
                setErrMsg(nm)
            }
        }, 500)

        return () => {
            window.clearInterval(timer)
            leaveFn()
        }
    }, [])

    async function send() {
        const text = draft.trim()
        if (text === "" || sending) {
            return
        }
        setSending(true)
        try {
            const resp = await fetch("/chat/post", {
                method: "POST",
                headers: {"Content-Type": "application/json"},
                body: JSON.stringify({RoomId: cfg.roomId, Sender: cfg.sender, Text: text}),
            })
            if (!resp.ok) {
                throw new Error("status " + resp.status)
            }
            setDraft("")
            // 不在这里乐观插入: 消息会通过 ws onChange 快路径回来, 避免重复.
        } catch (e) {
            setErrMsg("发送失败: " + String(e))
        } finally {
            setSending(false)
        }
    }

    return (
        <div style={{maxWidth: 600, margin: "20px auto", fontFamily: "sans-serif"}}>
            <h2>WebChat 房间 {cfg.roomId}</h2>
            <div>
                我是: <b>{cfg.sender}</b>　状态: <span id="status">{status}</span>
            </div>
            {errMsg !== "" && (
                <div id="errBanner" style={{color: "#c00", margin: "8px 0"}}>
                    ⚠ {errMsg}（请刷新页面重试）
                </div>
            )}
            <ul id="msgList" style={{border: "1px solid #ccc", minHeight: 200, padding: 8, listStyle: "none"}}>
                {messages.map((m) => (
                    <li className="msg" data-idx={m.MsgIndex} key={m.MsgIndex}>
                        <b>{m.Sender}</b>: {m.Text}
                    </li>
                ))}
            </ul>
            <div style={{display: "flex", gap: 8}}>
                <input
                    id="msgInput"
                    style={{flex: 1}}
                    value={draft}
                    placeholder="说点什么..."
                    onChange={(e) => setDraft(e.target.value)}
                    onKeyDown={(e) => {
                        if (e.key === "Enter") {
                            void send()
                        }
                    }}
                />
                <button id="sendBtn" disabled={sending} onClick={() => void send()}>
                    发送
                </button>
            </div>
        </div>
    )
}

const rootEl = document.getElementById("root")
if (rootEl) {
    createRoot(rootEl).render(<App/>)
}
