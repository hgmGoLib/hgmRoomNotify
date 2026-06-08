/*
* hgmRoomNotify 浏览器客户端对外的数据类型与常量.
* 这些类型是客户端使用方会直接接触到的: 房间变化事件/拒绝事件/观测事件/ui状态.
 */

// 可观测性事件类型. 和 golang 端 hgmRn_ObsEventType_t 值一致.
export type hgmRn_ObsEventType_t = number
export const hgmRn_ObsEventType_clientConnDialing: hgmRn_ObsEventType_t = 20
export const hgmRn_ObsEventType_clientConnConnected: hgmRn_ObsEventType_t = 21
export const hgmRn_ObsEventType_clientConnClose: hgmRn_ObsEventType_t = 25
export const hgmRn_ObsEventType_clientServerCloseConn: hgmRn_ObsEventType_t = 26
export const hgmRn_ObsEventType_clientNeedManual: hgmRn_ObsEventType_t = 27

// 服务端拒绝事件. IsConn=true 连接级拒绝(identity 认证失败); IsConn=false 房间级拒绝(进入 RoomId 被拒).
export interface hgmRn_ClientDeny_t {
    IsConn: boolean
    RoomId: string
    Reason: string
}

// 可观测性事件.
export interface hgmRn_ObsEvent_t {
    Type: hgmRn_ObsEventType_t
    CloseReason: string
    CloseDetail: string
}

// 房间变化事件.
export interface hgmRn_RoomOnChange_t{
    RoomId:string
    RoomEpoch:string
    ChangeSeq:number
    CVersionId:string
    LiveData:Uint8Array|null // 长度为0或null表示本次没有传输.
    ErrMsg:string // 回调中设置此字段表示报错. 非空时客户端进入 needManual 状态.
}

// 面向终端用户的ui状态.
export const hgmRn_UiStatusToUser_synced = "synced"
export const hgmRn_UiStatusToUser_syncing = "syncing"
export const hgmRn_UiStatusToUser_offline = "offline"
export const hgmRn_UiStatusToUser_needManual = "needManual"

// 默认观测函数. 只输出异常/正确性相关事件到 console.warn.
// 设置为 null 表示全局关闭默认观测.
export let hgmRn_ObsDefaultFn: ((ev: hgmRn_ObsEvent_t) => void) | null = function(ev: hgmRn_ObsEvent_t) {
    switch (ev.Type) {
    case hgmRn_ObsEventType_clientServerCloseConn:
        console.warn("hgmRoomNotify: clientServerCloseConn reason=" + ev.CloseDetail)
        break
    case hgmRn_ObsEventType_clientNeedManual:
        console.warn("hgmRoomNotify: clientNeedManual " + ev.CloseDetail)
        break
    case hgmRn_ObsEventType_clientConnClose:
        if (ev.CloseReason !== "noNeed" && ev.CloseReason !== "stopListen" && ev.CloseReason !== "") {
            console.warn("hgmRoomNotify: clientConnClose reason=" + ev.CloseReason)
        }
        break
    }
}
