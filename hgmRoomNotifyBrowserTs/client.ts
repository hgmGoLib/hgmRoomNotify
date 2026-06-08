/*
* 这里是websocket 状态同步的参考 浏览器实现.
* 该实现下,客户端ping使用轮询实现.
* 协议格式: 二进制, 和 golang 端一致.
* 数据类型见 types.ts, 线协议编解码见 protocol.ts.
 */
import {asyncSleep} from "./async.ts";
import {pathJoin, pathRemoveLastSection} from "./path.ts";
import {
    ObsEvent_t,
    ClientDeny_t,
    RoomOnChange_t,
    ObsDefaultFn,
    ObsEventType_clientConnDialing,
    ObsEventType_clientConnConnected,
    ObsEventType_clientConnClose,
    ObsEventType_clientServerCloseConn,
    ObsEventType_clientNeedManual,
    UiStatusToUser_synced,
    UiStatusToUser_syncing,
    UiStatusToUser_offline,
    UiStatusToUser_needManual,
} from "./types.ts";
import {
    Cmd_ping,
    Cmd_setTimeCfg,
    Cmd_roomEnter,
    Cmd_roomLeave,
    Cmd_roomValue,
    Cmd_connAllow,
    Cmd_deny,
    Cmd_closeConn,
    DenyScope_conn,
    marshalPing,
    marshalIdentity,
    marshalRoomCmd,
    readUint16LE,
    readInt64LEAsMs,
    readStr16LE,
    readBytes16LE,
    readUvarint,
    decodeUtf8,
} from "./protocol.ts";

// 默认全局变量实现
export function ClientDefault():Client{
    if (g_clientDefault!=null){
        return g_clientDefault
    }
    g_clientDefault = new Client()
    return g_clientDefault
}

export class Client{
    setUrl(url:string){
        url = handleUrl(url)
        this.url = url
        return this
    }
    // 加入房间. 返回离开函数.
    roomEnter(roomId:string,onChangeFn:(ev:RoomOnChange_t)=>void):(()=>void){
        if (this.authDenyFatal){
            // 已处于被服务端拒绝且未处理的致命状态. roomEnter 无效果.
            return ()=>{}
        }
        if (this.connDenyLocal){
            // 当前连接被连接级拒绝(已注册 OnDenyFn). 本地直接回 OnDenyFn, 不找服务端.
            if (this.OnDenyFn!==null){
                this.OnDenyFn({ IsConn:true, RoomId:roomId, Reason:"conn denied" })
            }
            return ()=>{}
        }
        let room = this.roomMap.get(roomId);
        if (room==null){
            room = new room_t()
            this.roomMap.set(roomId,room);
            this._sendRoomEnterMsg(roomId)
        }else{
            // 已有房间,直接回调当前版本.
            const ev:RoomOnChange_t = {
                RoomId: roomId,
                RoomEpoch: room.RoomEpoch,
                ChangeSeq: room.ChangeSeq,
                CVersionId: room.CVersionId,
                LiveData: null,
                ErrMsg: "",
            }
            this._callOnChangeFnSafe(onChangeFn,ev)
        }
        room.onChangeSet.add(onChangeFn);
        this._init_afterEnter();
        return ()=>{
            room.onChangeSet.delete(onChangeFn)
            if (room.onChangeSet.size==0 && this.roomMap.get(roomId)===room){
                this.roomMap.delete(roomId)
                this._sendRoomLeaveMsg(roomId)
            }
            if (this.roomMap.size==0){
                this.noNeedIdleCloseTimer.reset(this.timeoutCfg.ClientNoNeedIdleDur)
            }
        }
    }
    // 设置是否停止监听.
    SetIsStopListen(isStop:boolean){
        if (this.isStopListen===isStop){
            return
        }
        this.isStopListen = isStop
        if (isStop){
            this._close_thisSocket("stopListen")
        }else{
            this._init_afterEnter()
        }
    }
    GetIsStopListen():boolean{
        return this.isStopListen
    }
    HasNeed():boolean{
        return this.roomMap.size>0
    }
    IsConnectedSucc():boolean{
        return this.socket!=null && this.socket.readyState===1
    }
    GetWsDialNum():number{
        return this.wsDialNum
    }
    // 获取距离上次收到服务端有效消息的毫秒数. 从未收到过时返回 -1.
    GetSinceLastServerConfirm():number{
        if (this.lastReadSuccTime===0){
            return -1
        }
        return nowUnixMilli() - this.lastReadSuccTime
    }
    // 获取 needManual 状态的原因文本. 空字符串表示不在 needManual 状态.
    GetNeedManualMsg():string{
        return this.needManualMsg
    }
    // 获取当前订阅的房间数量.
    GetRoomCount():number{
        return this.roomMap.size
    }
    // 观测事件回调. null 表示使用 ObsDefaultFn. 设置为空函数表示关闭观测.
    ObsFn: ((ev: ObsEvent_t) => void) | null = null
    // 服务端拒绝(连接或房间)的处理回调. 可选.
    // 不注册时: 服务端发来 deny -> 判定为对接错误, 断开且不再重连, 后续 roomEnter 无效果.
    // 注册后默认: 连接级被拒 -> 连接保持/继续重连但进/离房间无效果, 新 roomEnter 本地直接回调; 房间级被拒 -> 房间留在 intent 随重连重试.
    OnDenyFn: ((ev: ClientDeny_t) => void) | null = null
    // in-band identity(类似 sessionId/token). 每次(重)连后发给服务端. 可空. 本模块不解析.
    setIdentity(identity:string){
        this.identity = identity
        return this
    }
    private identity:string = ""
    private connApproved = false   // 当前连接是否已通过连接级认证(收到 Cmd_connAllow).
    private connDenyLocal = false  // 当前连接被连接级拒绝(已注册 OnDenyFn). roomEnter 本地直接回调.
    private authDenyFatal = false  // 服务端 deny 但没注册 OnDenyFn 的致命状态. 断开且不再重连.
    private _obsCloseReason: string = ""
    private needManualMsg: string = ""
    private onChangeRunningCount: number = 0
    private url:string = ""
    private socket?:WebSocket
    private sendKeepAliveTimer:timer_t
    private noNeedIdleCloseTimer:timer_t
    private readToReconnectTimer:timer_t
    private wsDialTimer:timer_t
    private timeoutCfg = new TimeoutCfg_t()
    private roomMap= new Map<string,room_t>()
    private lastStartConnectTime = 0
    private lastWriteSuccTime = 0
    private lastReadSuccTime =0
    private lastKeepAliveSendTime = 0
    private isConnThreadRunning=false
    private wsDialNum = 0;
    private isStopListen = false;
    // 安全调用 onChangeFn, 捕获异常和 ErrMsg.
    private _callOnChangeFnSafe(fn:(ev:RoomOnChange_t)=>void, ev:RoomOnChange_t){
        this.onChangeRunningCount++
        try{
            fn(ev)
        }catch(e){
            this.needManualMsg = "onChangeFn throw: "+String(e)
        }finally{
            this.onChangeRunningCount--
        }
        if (ev.ErrMsg!==""){
            this.needManualMsg = "onChangeFn ErrMsg: "+ev.ErrMsg
        }
    }
    // 获取当前面向终端用户的ui状态.
    GetUiStatusToUser():string{
        if (this.needManualMsg!==""){
            return UiStatusToUser_needManual
        }
        if (this.isStopListen){
            return UiStatusToUser_needManual
        }
        const now = nowUnixMilli()
        const sinceLastRead = now - this.lastReadSuccTime
        if (sinceLastRead >= this.timeoutCfg.ClientLastReadToUiNoWorkDur){
            return UiStatusToUser_offline
        }
        const isWsConnected = this.socket!=null && this.socket.readyState===1
        if (sinceLastRead >= this.timeoutCfg.ClientLastReadToReconnectDur || this.onChangeRunningCount>0 || !isWsConnected){
            return UiStatusToUser_syncing
        }
        return UiStatusToUser_synced
    }
    _init_afterEnter(){
        this.noNeedIdleCloseTimer?.stop();
        if (this.isConnThreadRunning){
            return;
        }
        if (this.isStopListen){
            return;
        }
        this.isConnThreadRunning = true;
        (async ()=>{
            for(;;){
                if (this.isStopListen){
                    break;
                }
                if (this.HasNeed()==false){
                    break;
                }
                await this._tryConnOnceSync()
            }
            this.isConnThreadRunning = false;
        })();
    }
    _postRead(){
        this.lastReadSuccTime = nowUnixMilli()
        this.readToReconnectTimer.reset(this.timeoutCfg.ClientLastReadToReconnectDur)
        this._resetClientIdleToSendKeepAlive()
    }
    async _tryConnOnceSync(){
        this._close_thisSocket()
        // 控制开始连接的最小时间间隔。
        const dur = this.timeoutCfg.ClientReconnectMinDur - (nowUnixMilli()-this.lastStartConnectTime);
        if (dur>0){
            await asyncSleep(dur)
        }
        if (this.HasNeed()==false){
            return;
        }
        this.lastStartConnectTime = nowUnixMilli();
        this.lastKeepAliveSendTime = nowUnixMilli();
        this.wsDialNum++;
        this._obsCloseReason = ""
        this._emitObs({ Type: ObsEventType_clientConnDialing, CloseReason: "", CloseDetail: "" })
        const thisSocket = new WebSocket(this.url);
        thisSocket.binaryType = "arraybuffer"
        // 连接超时
        this.wsDialTimer.reset(this.timeoutCfg.ClientWsDialTimeoutDur)
        this.socket = thisSocket;
        thisSocket.addEventListener("message",(event)=>{
            this._postRead()
            const data = new Uint8Array(event.data as ArrayBuffer)
            this._processMessages(data)
        })
        thisSocket.addEventListener("open",()=>{
            this.wsDialTimer.stop()
            this.lastKeepAliveSendTime = nowUnixMilli()
            this.lastReadSuccTime = nowUnixMilli()
            this.lastWriteSuccTime = nowUnixMilli()
            this._resetClientIdleToSendKeepAlive()
            this.readToReconnectTimer.reset(this.timeoutCfg.ClientLastReadToReconnectDur);
            this._emitObs({ Type: ObsEventType_clientConnConnected, CloseReason: "", CloseDetail: "" })
            // 本次连接还没认证通过. 先发 identity(永远发), 等 Cmd_connAllow 后再发 roomEnter.
            this.connApproved = false
            this._sendBinaryMsg(marshalIdentity(this.identity))
        })
        // 等待 socket 关闭.
        await new Promise<void>((resolve)=>{
            thisSocket.addEventListener("close",()=>{
                if (this.socket===thisSocket){
                    this.socket = undefined;
                }
                this.sendKeepAliveTimer.stop()
                this.wsDialTimer.stop()
                this.readToReconnectTimer.stop()
                this.noNeedIdleCloseTimer.stop()
                this._emitObs({ Type: ObsEventType_clientConnClose, CloseReason: this._obsCloseReason, CloseDetail: "" })
                resolve();
            });
        })
    }
    // 解析一个websocket message中的多个Msg.
    _processMessages(data:Uint8Array){
        let pos = 0
        while(pos<data.length){
            if (pos+2>data.length){
                this._close_thisSocket("protocolError")
                return
            }
            const frameLen = readUint16LE(data,pos)
            pos+=2
            if (frameLen===0 || pos+frameLen>data.length){
                this._close_thisSocket("protocolError")
                return
            }
            const frameData = data.subarray(pos,pos+frameLen)
            pos+=frameLen
            this._processOneMsg(frameData)
        }
    }
    // 解析并处理单条Msg.
    _processOneMsg(data:Uint8Array){
        if (data.length<1){
            this._close_thisSocket("protocolError")
            return
        }
        const cmd = data[0]
        switch(cmd){
        case Cmd_ping:
            // 服务端回复的ping,不需要处理.
            break
        case Cmd_setTimeCfg:{
            if (data.length<2){
                this._close_thisSocket("protocolError")
                return
            }
            let p = 1
            const count = data[p]; p++
            if (p + count * 9 > data.length){
                this._close_thisSocket("protocolError")
                return
            }
            const cfg = new TimeoutCfg_t()
            for (let i = 0; i < count; i++){
                const fieldId = data[p]; p++
                const val = readInt64LEAsMs(data, p); p += 8
                switch (fieldId){
                case 1: cfg.ClientReconnectMinDur = val; break
                case 2: cfg.ClientIdleToSendKeepAliveDur = val; break
                case 3: cfg.ClientLastReadToReconnectDur = val; break
                case 4: cfg.ClientWsDialTimeoutDur = val; break
                case 5: cfg.ClientNoNeedIdleDur = val; break
                case 6: cfg.ClientLastReadToUiNoWorkDur = val; break
                // 不认识的fieldId跳过, 保持前向兼容.
                }
            }
            cfg._applyMinimum()
            this.timeoutCfg = cfg
            break
        }
        case Cmd_roomValue:{
            let p = 1
            const rv = readStr16LE(data,p)
            if (rv===null){ this._close_thisSocket("protocolError"); return }
            const roomId = rv.s; p = rv.pos
            // RoomEpoch: uint8长度+内容
            if (p+1>data.length){ this._close_thisSocket("protocolError"); return }
            const roomEpochLen = data[p]; p++
            if (p+roomEpochLen>data.length){ this._close_thisSocket("protocolError"); return }
            const roomEpoch = roomEpochLen>0 ? decodeUtf8(data.subarray(p,p+roomEpochLen)) : ""
            p+=roomEpochLen
            // ChangeSeq: uvarint
            const uvr = readUvarint(data,p)
            if (uvr===null){ this._close_thisSocket("protocolError"); return }
            const changeSeq = uvr.value; p = uvr.pos
            // CVersionId: uint8长度+内容
            if (p+1>data.length){ this._close_thisSocket("protocolError"); return }
            const cVersionIdLen = data[p]; p++
            if (p+cVersionIdLen>data.length){ this._close_thisSocket("protocolError"); return }
            const cVersionId = cVersionIdLen>0 ? decodeUtf8(data.subarray(p,p+cVersionIdLen)) : ""
            p+=cVersionIdLen
            // LiveData: uint16LE长度+内容
            const rv3 = readBytes16LE(data,p)
            if (rv3===null){ this._close_thisSocket("protocolError"); return }
            const liveData = rv3.bytes

            const room = this.roomMap.get(roomId)
            if (room===undefined){
                return
            }
            if (roomEpoch!==room.RoomEpoch){
                // 房间纪元变了(房间被重建或服务器重启), 无条件接受.
                room.RoomEpoch = roomEpoch
                room.ChangeSeq = changeSeq
                room.CVersionId = cVersionId
            }else if (changeSeq>room.ChangeSeq){
                // 同纪元有新变化.
                room.ChangeSeq = changeSeq
                room.CVersionId = cVersionId
            }else{
                // 旧消息(竞争产生的), 整条忽略.
                return
            }
            const ev:RoomOnChange_t = {
                RoomId: roomId,
                RoomEpoch: room.RoomEpoch,
                ChangeSeq: room.ChangeSeq,
                CVersionId: room.CVersionId,
                LiveData: liveData.length>0 ? liveData : null,
                ErrMsg: "",
            }
            // 复制一份, 避免回调中调用 leaveFn 修改 onChangeSet 导致迭代异常.
            const cbList = [...room.onChangeSet]
            for (const cb of cbList){
                this._callOnChangeFnSafe(cb,ev)
            }
            break
        }
        case Cmd_connAllow:{
            // [uint8 AuthEnabled]
            const authEnabled = data.length>1 ? data[1]!==0 : false
            this.connDenyLocal = false
            if (authEnabled && this.OnDenyFn===null){
                this._emitObs({ Type: ObsEventType_clientNeedManual, CloseReason: "", CloseDetail: "server has auth enabled but OnDenyFn is null" })
            }
            this.connApproved = true
            // 认证通过, 把当前所有房间发出去.
            this.roomMap.forEach((_,roomId)=>{
                this._sendRoomEnterMsg(roomId)
            })
            break
        }
        case Cmd_deny:{
            // [uint8 DenyScope][uint16LE RoomId][uint16LE Reason]
            let p = 1
            if (p+1>data.length){ this._close_thisSocket("protocolError"); return }
            const scope = data[p]; p++
            const rv = readStr16LE(data,p)
            if (rv===null){ this._close_thisSocket("protocolError"); return }
            const roomId = rv.s; p = rv.pos
            const rv2 = readStr16LE(data,p)
            if (rv2===null){ this._close_thisSocket("protocolError"); return }
            const reason = rv2.s
            if (this.OnDenyFn===null){
                // 没注册 OnDenyFn: 对接错误. 断开且不再重连, needManual.
                this.authDenyFatal = true
                this.needManualMsg = "被服务端拒绝, 且客户端没有处理(未注册 OnDenyFn). reason="+reason
                this._emitObs({ Type: ObsEventType_clientNeedManual, CloseReason: "", CloseDetail: this.needManualMsg })
                this.SetIsStopListen(true)
                return
            }
            if (scope===DenyScope_conn){
                this.connDenyLocal = true
                this.OnDenyFn({ IsConn:true, RoomId:"", Reason:reason })
            }else{
                this.OnDenyFn({ IsConn:false, RoomId:roomId, Reason:reason })
            }
            break
        }
        case Cmd_closeConn:{
            // [uint8 IsTemp][uint16LE Reason]
            let p = 1
            if (p+1>data.length){ this._close_thisSocket("protocolError"); return }
            const isTemp = data[p]!==0; p++
            const rv = readStr16LE(data,p)
            const reason = rv!==null ? rv.s : ""
            if (isTemp){
                // 临时关闭: 关掉当前连接, 重连循环会自动重连(重连重新认证).
                this._close_thisSocket("serverCloseConnTemp")
            }else{
                // 永久关闭: 不再重连. 预期的正常终态(带 reason).
                this.SetIsStopListen(true)
                this._emitObs({ Type: ObsEventType_clientServerCloseConn, CloseReason: "", CloseDetail: reason })
            }
            break
        }
        }
    }
    _close_thisSocket(reason?: string){
        if (reason !== undefined){
            this._obsCloseReason = reason
        }
        if (this.socket!=null){
            this.socket.close()
            this.socket = undefined;
        }
    }
    private _emitObs(ev: ObsEvent_t){
        const fn = this.ObsFn ?? ObsDefaultFn
        if (fn != null){
            fn(ev)
        }
    }
    constructor(){
        this.sendKeepAliveTimer = newTimer(()=>{
            this.lastKeepAliveSendTime = nowUnixMilli()
            this._sendBinaryMsg(marshalPing())
        })
        this.noNeedIdleCloseTimer = newTimer(()=>{
            this._close_thisSocket("noNeed")
        })
        this.readToReconnectTimer = newTimer(()=>{
            this._close_thisSocket("readTimeout")
        })
        this.wsDialTimer = newTimer(()=>{
            this._close_thisSocket("dialTimeout")
        })
    }
    // 发送已经序列化好的单条Msg(带uint16长度前缀).
    _sendBinaryMsg(framedMsg:Uint8Array):void{
        if (this.socket!=null && this?.socket.readyState===1){ // OPEN
            this.socket.send(framedMsg);
            this.lastWriteSuccTime = nowUnixMilli()
            this._resetClientIdleToSendKeepAlive()
        }
    }
    _sendRoomEnterMsg(roomId:string){
        // 认证通过前不发 roomEnter(连上后由 Cmd_connAllow 触发批量发送).
        if (!this.connApproved){
            return
        }
        this._sendBinaryMsg(marshalRoomCmd(Cmd_roomEnter,roomId))
    }
    _sendRoomLeaveMsg(roomId:string){
        this._sendBinaryMsg(marshalRoomCmd(Cmd_roomLeave,roomId))
    }
    _resetClientIdleToSendKeepAlive(){
        let now = nowUnixMilli()
        const dur = this.timeoutCfg.ClientIdleToSendKeepAliveDur
        let t1 = this.lastKeepAliveSendTime+dur
        let t2 = this.lastWriteSuccTime +dur
        let t3 = this.lastReadSuccTime + dur
        let minOfT2AndT3 = t2;
        if (minOfT2AndT3>t3){
            minOfT2AndT3 = t3;
        }
        if (minOfT2AndT3>t1){
            t1 = minOfT2AndT3;
        }
        let dur2 = t1-now
        this.sendKeepAliveTimer?.reset(dur2);
    }
}

function newTimer(fn:()=>void):timer_t{
    let timeId:null|number = null;
    return {
        reset(dur:number){
            if (timeId!==null){
                clearTimeout(timeId);
            }
            timeId = setTimeout(fn,dur);
        },
        stop(){
            if (timeId!==null){
                clearTimeout(timeId);
            }
        }
    }
}
declare interface timer_t{
    reset:(dur:number)=>void
    stop:()=>void
}
let g_clientDefault:Client|undefined = undefined

class TimeoutCfg_t{
    // 此处单位 毫秒. (注意从 golang/ws 过来的时候,golang time.Duration 是纳秒,需要除以 1e6)
    ClientReconnectMinDur:number = 5000
    ClientIdleToSendKeepAliveDur:number = 4500
    ClientLastReadToReconnectDur:number = 10000
    ClientWsDialTimeoutDur:number = 5000
    ClientNoNeedIdleDur:number = 20000
    ClientLastReadToUiNoWorkDur:number = 10000
    // 确保最小值100ms,和golang端InitWithDefault一致.
    _applyMinimum(){
        const min = 100
        if (this.ClientReconnectMinDur<min) this.ClientReconnectMinDur = min
        if (this.ClientIdleToSendKeepAliveDur<min) this.ClientIdleToSendKeepAliveDur = min
        if (this.ClientLastReadToReconnectDur<min) this.ClientLastReadToReconnectDur = min
        if (this.ClientWsDialTimeoutDur<min) this.ClientWsDialTimeoutDur = min
        if (this.ClientNoNeedIdleDur<min) this.ClientNoNeedIdleDur = min
        if (this.ClientLastReadToUiNoWorkDur<min) this.ClientLastReadToUiNoWorkDur = min
    }
}

class room_t{
    RoomEpoch:string = ""
    ChangeSeq:number = 0
    CVersionId:string = ""
    onChangeSet= new Set<(ev:RoomOnChange_t)=>void>
}
/*
* 支持三种输入:
    * wss://xxx.com/xxx
    * xxx
    * /xxx/xxx
 */
function handleUrl(url:string):string{
    if (url.includes("://")){
        return url;
    }
    let outUrl = ""
    if (location.protocol==="https:"){ // like https:
        outUrl = "wss://"
    }else{
        outUrl = "ws://"
    }
    outUrl+=location.host // like 10.0.0.1:1900
    if (url.startsWith("/")){
        return outUrl+url
    }
    const path = pathRemoveLastSection(location.pathname)
    return outUrl+pathJoin(path,url)
}
function nowUnixMilli():number{
    return new Date().getTime()
}
