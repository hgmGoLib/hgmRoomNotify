/*
* hgmRoomNotify 二进制线协议的编解码. 和 golang 端一致.
* 一个websocket message内可以包含多个Msg, 格式: [uint16LE len][msg bytes][uint16LE len][msg bytes]...
* 这里只放与具体连接状态无关的纯函数(序列化/反序列化/字节读写), 方便单独阅读和测试.
 */

// 二进制协议 Cmd 常量. 和 golang 端一致.
export const Cmd_ping:number = 1
export const Cmd_setTimeCfg:number = 2
export const Cmd_roomEnter:number = 3
export const Cmd_roomLeave:number = 4
export const Cmd_roomValue:number = 5
export const Cmd_identity:number = 6
export const Cmd_connAllow:number = 7
export const Cmd_deny:number = 8
export const Cmd_closeConn:number = 9
export const DenyScope_conn:number = 1
export const DenyScope_room:number = 2

// 序列化 ping 消息. 带uint16长度前缀.
export function marshalPing():Uint8Array{
    // [uint16LE frameLen=1][uint8 cmd=1]
    const buf = new Uint8Array(3)
    buf[0]=1; buf[1]=0 // frameLen=1
    buf[2]=Cmd_ping
    return buf
}
// 序列化 identity 消息. 带uint16长度前缀. [uint16LE frameLen][uint8 cmd][uint16LE idLen][id]
export function marshalIdentity(identity:string):Uint8Array{
    const idBytes = encodeUtf8(identity)
    const msgLen = 1 + 2 + idBytes.length
    const buf = new Uint8Array(2+msgLen)
    writeUint16LE(buf,0,msgLen)
    let pos = 2
    buf[pos] = Cmd_identity; pos++
    writeUint16LE(buf,pos,idBytes.length); pos+=2
    buf.set(idBytes,pos)
    return buf
}
// 序列化 roomEnter/roomLeave 消息. 带uint16长度前缀.
export function marshalRoomCmd(cmd:number, roomId:string):Uint8Array{
    const roomIdBytes = encodeUtf8(roomId)
    const msgLen = 1 + 2 + roomIdBytes.length
    const buf = new Uint8Array(2+msgLen)
    writeUint16LE(buf,0,msgLen)
    let pos = 2
    buf[pos] = cmd; pos++
    writeUint16LE(buf,pos,roomIdBytes.length); pos+=2
    buf.set(roomIdBytes,pos)
    return buf
}

// 读取 uint16 LE.
export function readUint16LE(data:Uint8Array,pos:number):number{
    return data[pos] | (data[pos+1]<<8)
}
// 写入 uint16 LE.
export function writeUint16LE(data:Uint8Array,pos:number,val:number){
    data[pos] = val & 0xFF
    data[pos+1] = (val>>8) & 0xFF
}
// 读取 int64 LE 并转换为毫秒(golang time.Duration 是纳秒).
export function readInt64LEAsMs(data:Uint8Array,pos:number):number{
    const view = new DataView(data.buffer,data.byteOffset+pos,8)
    const lo = view.getUint32(0,true)
    const hi = view.getInt32(4,true)
    // 合成为 number (精度在 53 bit 内 对 time.Duration 来说足够)
    const nanos = hi * 0x100000000 + lo
    return nanos / 1e6
}
// 读取 uint16LE长度前缀 + utf8字符串.
export function readStr16LE(data:Uint8Array,pos:number):{s:string,pos:number}|null{
    if (pos+2>data.length) return null
    const len = readUint16LE(data,pos); pos+=2
    if (pos+len>data.length) return null
    const s = len>0 ? decodeUtf8(data.subarray(pos,pos+len)) : ""
    return {s, pos:pos+len}
}
// 读取 uint16LE长度前缀 + 二进制数据.
export function readBytes16LE(data:Uint8Array,pos:number):{bytes:Uint8Array,pos:number}|null{
    if (pos+2>data.length) return null
    const len = readUint16LE(data,pos); pos+=2
    if (pos+len>data.length) return null
    const bytes = len>0 ? data.slice(pos,pos+len) : new Uint8Array(0)
    return {bytes, pos:pos+len}
}

// 读取 uvarint (变长无符号整数). 返回null表示数据不足或格式错误.
export function readUvarint(data:Uint8Array,pos:number):{value:number,pos:number}|null{
    let x = 0
    let s = 0
    for(;;){
        if (pos>=data.length) return null
        const b = data[pos]; pos++
        if (b<0x80){
            // JS number 精度 53 bit, 这里不会超过 (ChangeSeq 实际值远小于 2^53)
            return {value: x + b * (2**s), pos}
        }
        if (s>=49){ // 超过 JS number 安全整数范围
            return null
        }
        x += (b & 0x7f) * (2**s)
        s += 7
    }
}

const textEncoder = new TextEncoder()
const textDecoder = new TextDecoder()
export function encodeUtf8(s:string):Uint8Array{
    return textEncoder.encode(s)
}
export function decodeUtf8(data:Uint8Array):string{
    return textDecoder.decode(data)
}
