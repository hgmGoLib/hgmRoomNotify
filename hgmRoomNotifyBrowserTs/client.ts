/*
* 这里是websocket 状态同步的参考 浏览器实现.
* 该实现下,客户端ping使用轮询实现.
* 协议格式: 二进制, 和 golang 端一致.
* 数据类型见 types.ts, 线协议编解码见 protocol.ts.
 */
import {hgmRn_asyncSleep} from "./async.ts";
import {hgmRn_pathJoin, hgmRn_pathRemoveLastSection} from "./path.ts";
import {
    hgmRn_ObsEvent_t,
    hgmRn_ClientDeny_t,
    hgmRn_RoomOnChange_t,
    hgmRn_ObsDefaultFn,
    hgmRn_ObsEventType_clientConnDialing,
    hgmRn_ObsEventType_clientConnConnected,
    hgmRn_ObsEventType_clientConnClose,
    hgmRn_ObsEventType_clientServerCloseConn,
    hgmRn_ObsEventType_clientNeedManual,
    hgmRn_UiStatusToUser_synced,
    hgmRn_UiStatusToUser_syncing,
    hgmRn_UiStatusToUser_offline,
    hgmRn_UiStatusToUser_needManual,
} from "./types.ts";
import {
    hgmRn_Cmd_ping,
    hgmRn_Cmd_setTimeCfg,
    hgmRn_Cmd_roomEnter,
    hgmRn_Cmd_roomLeave,
    hgmRn_Cmd_roomValue,
    hgmRn_Cmd_roomValueMore,
    hgmRn_Cmd_connAllow,
    hgmRn_Cmd_deny,
    hgmRn_Cmd_closeConn,
    hgmRn_DenyScope_conn,
    hgmRn_marshalPing,
    hgmRn_marshalIdentity,
    hgmRn_marshalRoomCmd,
    hgmRn_readUint16LE,
    hgmRn_readInt64LEAsMs,
    hgmRn_readStr16LE,
    hgmRn_readBytes16LE,
    hgmRn_readUvarint,
    hgmRn_decodeUtf8,
} from "./protocol.ts";

// 默认全局变量实现
export function hgmRn_ClientDefault():hgmRn_Client{
    if (g_hgmRn_clientDefault!=null){
        return g_hgmRn_clientDefault
    }
    g_hgmRn_clientDefault = new hgmRn_Client()
    return g_hgmRn_clientDefault
}

export class hgmRn_Client{
    setUrl(url:string){
        url = hgmRn_handleUrl(url)
        this.url = url
        return this
    }
    // 加入房间. 返回离开函数.
    roomEnter(roomId:string,onChangeFn:(ev:hgmRn_RoomOnChange_t)=>void):(()=>void){
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
            room = new hgmRn_room_t()
            this.roomMap.set(roomId,room);
            this._sendRoomEnterMsg(roomId)
        }else{
            // 已有房间,直接回调当前版本.
            const ev:hgmRn_RoomOnChange_t = {
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
        return hgmRn_nowUnixMilli() - this.lastReadSuccTime
    }
    // 获取 needManual 状态的原因文本. 空字符串表示不在 needManual 状态.
    GetNeedManualMsg():string{
        return this.needManualMsg
    }
    // 获取当前订阅的房间数量.
    GetRoomCount():number{
        return this.roomMap.size
    }
    // 观测事件回调. null 表示使用 hgmRn_ObsDefaultFn. 设置为空函数表示关闭观测.
    ObsFn: ((ev: hgmRn_ObsEvent_t) => void) | null = null
    // 服务端拒绝(连接或房间)的处理回调. 可选.
    // 不注册时: 服务端发来 deny -> 判定为对接错误, 断开且不再重连, 后续 roomEnter 无效果.
    // 注册后默认: 连接级被拒 -> 连接保持/继续重连但进/离房间无效果, 新 roomEnter 本地直接回调; 房间级被拒 -> 房间留在 intent 随重连重试.
    OnDenyFn: ((ev: hgmRn_ClientDeny_t) => void) | null = null
    // in-band identity(类似 sessionId/token). 每次(重)连后发给服务端. 可空. 本模块不解析.
    setIdentity(identity:string){
        this.identity = identity
        return this
    }
    private identity:string = ""
    private connApproved = false   // 当前连接是否已通过连接级认证(收到 hgmRn_Cmd_connAllow).
    private connDenyLocal = false  // 当前连接被连接级拒绝(已注册 OnDenyFn). roomEnter 本地直接回调.
    private authDenyFatal = false  // 服务端 deny 但没注册 OnDenyFn 的致命状态. 断开且不再重连.
    private _obsCloseReason: string = ""
    private needManualMsg: string = ""
    private onChangeRunningCount: number = 0
    private url:string = ""
    private socket?:WebSocket
    private sendKeepAliveTimer:hgmRn_timer_t
    private noNeedIdleCloseTimer:hgmRn_timer_t
    private readToReconnectTimer:hgmRn_timer_t
    private wsDialTimer:hgmRn_timer_t
    private timeoutCfg = new hgmRn_TimeoutCfg_t()
    private roomMap= new Map<string,hgmRn_room_t>()
    private lastStartConnectTime = 0
    private lastWriteSuccTime = 0
    private lastReadSuccTime =0
    private lastKeepAliveSendTime = 0
    private isConnThreadRunning=false
    private wsDialNum = 0;
    private isStopListen = false;
    // 大 LiveData 分块重组缓冲. roomValueMore 累积分片, 末条 roomValue 拼出完整 LiveData 后清空. 每次新建连接时重置.
    private _roomValueReassembleChunks: Uint8Array[] = []
    private _roomValueReassembleLen = 0
    // 安全调用 onChangeFn, 捕获异常和 ErrMsg.
    private _callOnChangeFnSafe(fn:(ev:hgmRn_RoomOnChange_t)=>void, ev:hgmRn_RoomOnChange_t){
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
            return hgmRn_UiStatusToUser_needManual
        }
        if (this.isStopListen){
            return hgmRn_UiStatusToUser_needManual
        }
        const now = hgmRn_nowUnixMilli()
        const sinceLastRead = now - this.lastReadSuccTime
        if (sinceLastRead >= this.timeoutCfg.ClientLastReadToUiNoWorkDur){
            return hgmRn_UiStatusToUser_offline
        }
        const isWsConnected = this.socket!=null && this.socket.readyState===1
        if (sinceLastRead >= this.timeoutCfg.ClientLastReadToReconnectDur || this.onChangeRunningCount>0 || !isWsConnected){
            return hgmRn_UiStatusToUser_syncing
        }
        return hgmRn_UiStatusToUser_synced
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
        this.lastReadSuccTime = hgmRn_nowUnixMilli()
        this.readToReconnectTimer.reset(this.timeoutCfg.ClientLastReadToReconnectDur)
        this._resetClientIdleToSendKeepAlive()
    }
    async _tryConnOnceSync(){
        this._close_thisSocket()
        // 控制开始连接的最小时间间隔。
        const dur = this.timeoutCfg.ClientReconnectMinDur - (hgmRn_nowUnixMilli()-this.lastStartConnectTime);
        if (dur>0){
            await hgmRn_asyncSleep(dur)
        }
        if (this.HasNeed()==false){
            return;
        }
        this.lastStartConnectTime = hgmRn_nowUnixMilli();
        this.lastKeepAliveSendTime = hgmRn_nowUnixMilli();
        this.wsDialNum++;
        this._obsCloseReason = ""
        this._emitObs({ Type: hgmRn_ObsEventType_clientConnDialing, CloseReason: "", CloseDetail: "" })
        const thisSocket = new WebSocket(this.url);
        thisSocket.binaryType = "arraybuffer"
        // 连接超时
        this.wsDialTimer.reset(this.timeoutCfg.ClientWsDialTimeoutDur)
        this.socket = thisSocket;
        this._roomValueReassembleChunks = []
        this._roomValueReassembleLen = 0
        thisSocket.addEventListener("message",(event)=>{
            this._postRead()
            const data = new Uint8Array(event.data as ArrayBuffer)
            this._processMessages(data)
        })
        thisSocket.addEventListener("open",()=>{
            this.wsDialTimer.stop()
            this.lastKeepAliveSendTime = hgmRn_nowUnixMilli()
            this.lastReadSuccTime = hgmRn_nowUnixMilli()
            this.lastWriteSuccTime = hgmRn_nowUnixMilli()
            this._resetClientIdleToSendKeepAlive()
            this.readToReconnectTimer.reset(this.timeoutCfg.ClientLastReadToReconnectDur);
            this._emitObs({ Type: hgmRn_ObsEventType_clientConnConnected, CloseReason: "", CloseDetail: "" })
            // 本次连接还没认证通过. 先发 identity(永远发), 等 hgmRn_Cmd_connAllow 后再发 roomEnter.
            this.connApproved = false
            this._sendBinaryMsg(hgmRn_marshalIdentity(this.identity))
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
                this._emitObs({ Type: hgmRn_ObsEventType_clientConnClose, CloseReason: this._obsCloseReason, CloseDetail: "" })
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
            const frameLen = hgmRn_readUint16LE(data,pos)
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
        case hgmRn_Cmd_ping:
            // 服务端回复的ping,不需要处理.
            break
        case hgmRn_Cmd_setTimeCfg:{
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
            const cfg = new hgmRn_TimeoutCfg_t()
            for (let i = 0; i < count; i++){
                const fieldId = data[p]; p++
                const val = hgmRn_readInt64LEAsMs(data, p); p += 8
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
        case hgmRn_Cmd_roomValue:{
            // 完整 roomValue: [RoomId][RoomEpoch][ChangeSeq][CVersionId][LiveData].
            // 前面有 roomValueMore 累积分片时, 本条即为分块序列的最后一片, 把累积分片拼到它前面再通知, 并清空重组缓冲.
            const f = this._parseRoomValueFields(data,1)
            if (f===null){ this._close_thisSocket("protocolError"); return }
            let liveData = f.chunk
            if (this._roomValueReassembleChunks.length>0){
                this._roomValueReassembleChunks.push(f.chunk)
                this._roomValueReassembleLen += f.chunk.length
                liveData = new Uint8Array(this._roomValueReassembleLen)
                let off = 0
                for (const c of this._roomValueReassembleChunks){ liveData.set(c,off); off += c.length }
                this._roomValueReassembleChunks = []
                this._roomValueReassembleLen = 0
            }
            this._onRoomValue(f.roomId,f.roomEpoch,f.changeSeq,f.cVersionId,liveData)
            break
        }
        case hgmRn_Cmd_roomValueMore:{
            // 累积一段 LiveData 分片(后面还有). [LiveData分片: uint16LE长度+内容]
            const rv = hgmRn_readBytes16LE(data,1)
            if (rv===null){ this._close_thisSocket("protocolError"); return }
            this._roomValueReassembleChunks.push(rv.bytes)
            this._roomValueReassembleLen += rv.bytes.length
            break
        }
        case hgmRn_Cmd_connAllow:{
            // [uint8 AuthEnabled]
            const authEnabled = data.length>1 ? data[1]!==0 : false
            this.connDenyLocal = false
            if (authEnabled && this.OnDenyFn===null){
                this._emitObs({ Type: hgmRn_ObsEventType_clientNeedManual, CloseReason: "", CloseDetail: "server has auth enabled but OnDenyFn is null" })
            }
            this.connApproved = true
            // 认证通过, 把当前所有房间发出去.
            this.roomMap.forEach((_,roomId)=>{
                this._sendRoomEnterMsg(roomId)
            })
            break
        }
        case hgmRn_Cmd_deny:{
            // [uint8 DenyScope][uint16LE RoomId][uint16LE Reason]
            let p = 1
            if (p+1>data.length){ this._close_thisSocket("protocolError"); return }
            const scope = data[p]; p++
            const rv = hgmRn_readStr16LE(data,p)
            if (rv===null){ this._close_thisSocket("protocolError"); return }
            const roomId = rv.s; p = rv.pos
            const rv2 = hgmRn_readStr16LE(data,p)
            if (rv2===null){ this._close_thisSocket("protocolError"); return }
            const reason = rv2.s
            if (this.OnDenyFn===null){
                // 没注册 OnDenyFn: 对接错误. 断开且不再重连, needManual.
                this.authDenyFatal = true
                this.needManualMsg = "被服务端拒绝, 且客户端没有处理(未注册 OnDenyFn). reason="+reason
                this._emitObs({ Type: hgmRn_ObsEventType_clientNeedManual, CloseReason: "", CloseDetail: this.needManualMsg })
                this.SetIsStopListen(true)
                return
            }
            if (scope===hgmRn_DenyScope_conn){
                this.connDenyLocal = true
                this.OnDenyFn({ IsConn:true, RoomId:"", Reason:reason })
            }else{
                this.OnDenyFn({ IsConn:false, RoomId:roomId, Reason:reason })
            }
            break
        }
        case hgmRn_Cmd_closeConn:{
            // [uint8 IsTemp][uint16LE Reason]
            let p = 1
            if (p+1>data.length){ this._close_thisSocket("protocolError"); return }
            const isTemp = data[p]!==0; p++
            const rv = hgmRn_readStr16LE(data,p)
            const reason = rv!==null ? rv.s : ""
            if (isTemp){
                // 临时关闭: 关掉当前连接, 重连循环会自动重连(重连重新认证).
                this._close_thisSocket("serverCloseConnTemp")
            }else{
                // 永久关闭: 不再重连. 预期的正常终态(带 reason).
                this.SetIsStopListen(true)
                this._emitObs({ Type: hgmRn_ObsEventType_clientServerCloseConn, CloseReason: "", CloseDetail: reason })
            }
            break
        }
        }
    }
    // 解析 roomValue 的元数据+数据字段: [RoomId][RoomEpoch][ChangeSeq][CVersionId][LiveData/分片]. 失败返回 null.
    _parseRoomValueFields(data:Uint8Array,startPos:number):{roomId:string,roomEpoch:string,changeSeq:number,cVersionId:string,chunk:Uint8Array}|null{
        let p = startPos
        const rv = hgmRn_readStr16LE(data,p)
        if (rv===null){ return null }
        const roomId = rv.s; p = rv.pos
        // RoomEpoch: uint8长度+内容
        if (p+1>data.length){ return null }
        const roomEpochLen = data[p]; p++
        if (p+roomEpochLen>data.length){ return null }
        const roomEpoch = roomEpochLen>0 ? hgmRn_decodeUtf8(data.subarray(p,p+roomEpochLen)) : ""
        p+=roomEpochLen
        // ChangeSeq: uvarint
        const uvr = hgmRn_readUvarint(data,p)
        if (uvr===null){ return null }
        const changeSeq = uvr.value; p = uvr.pos
        // CVersionId: uint8长度+内容
        if (p+1>data.length){ return null }
        const cVersionIdLen = data[p]; p++
        if (p+cVersionIdLen>data.length){ return null }
        const cVersionId = cVersionIdLen>0 ? hgmRn_decodeUtf8(data.subarray(p,p+cVersionIdLen)) : ""
        p+=cVersionIdLen
        // LiveData/分片: uint16LE长度+内容
        const rv3 = hgmRn_readBytes16LE(data,p)
        if (rv3===null){ return null }
        return {roomId,roomEpoch,changeSeq,cVersionId,chunk:rv3.bytes}
    }
    // 收到一次完整 roomValue(单条, 或分块重组后)后更新房间状态并通知 onChange 回调.
    _onRoomValue(roomId:string,roomEpoch:string,changeSeq:number,cVersionId:string,liveData:Uint8Array){
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
        const ev:hgmRn_RoomOnChange_t = {
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
    private _emitObs(ev: hgmRn_ObsEvent_t){
        const fn = this.ObsFn ?? hgmRn_ObsDefaultFn
        if (fn != null){
            fn(ev)
        }
    }
    constructor(){
        this.sendKeepAliveTimer = hgmRn_newTimer(()=>{
            this.lastKeepAliveSendTime = hgmRn_nowUnixMilli()
            this._sendBinaryMsg(hgmRn_marshalPing())
        })
        this.noNeedIdleCloseTimer = hgmRn_newTimer(()=>{
            this._close_thisSocket("noNeed")
        })
        this.readToReconnectTimer = hgmRn_newTimer(()=>{
            this._close_thisSocket("readTimeout")
        })
        this.wsDialTimer = hgmRn_newTimer(()=>{
            this._close_thisSocket("dialTimeout")
        })
    }
    // 发送已经序列化好的单条Msg(带uint16长度前缀).
    _sendBinaryMsg(framedMsg:Uint8Array):void{
        if (this.socket!=null && this?.socket.readyState===1){ // OPEN
            this.socket.send(framedMsg);
            this.lastWriteSuccTime = hgmRn_nowUnixMilli()
            this._resetClientIdleToSendKeepAlive()
        }
    }
    _sendRoomEnterMsg(roomId:string){
        // 认证通过前不发 roomEnter(连上后由 hgmRn_Cmd_connAllow 触发批量发送).
        if (!this.connApproved){
            return
        }
        this._sendBinaryMsg(hgmRn_marshalRoomCmd(hgmRn_Cmd_roomEnter,roomId))
    }
    _sendRoomLeaveMsg(roomId:string){
        this._sendBinaryMsg(hgmRn_marshalRoomCmd(hgmRn_Cmd_roomLeave,roomId))
    }
    _resetClientIdleToSendKeepAlive(){
        let now = hgmRn_nowUnixMilli()
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

function hgmRn_newTimer(fn:()=>void):hgmRn_timer_t{
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
declare interface hgmRn_timer_t{
    reset:(dur:number)=>void
    stop:()=>void
}
let g_hgmRn_clientDefault:hgmRn_Client|undefined = undefined

class hgmRn_TimeoutCfg_t{
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

class hgmRn_room_t{
    RoomEpoch:string = ""
    ChangeSeq:number = 0
    CVersionId:string = ""
    onChangeSet= new Set<(ev:hgmRn_RoomOnChange_t)=>void>
}
/*
* 支持三种输入:
    * wss://xxx.com/xxx
    * xxx
    * /xxx/xxx
 */
function hgmRn_handleUrl(url:string):string{
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
    const path = hgmRn_pathRemoveLastSection(location.pathname)
    return outUrl+hgmRn_pathJoin(path,url)
}
function hgmRn_nowUnixMilli():number{
    return new Date().getTime()
}
