package zlibIdGen

import (
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibEncodeB32"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibRandFast"
	"sync"
	"time"
)

/*
输出结果例子 17fatw9hnw1y8ge6emw3et3fmtu9d
字符串固定长度 42
id 生成方案3
==========
* 适合一般数据库对象的id生成.在没有人故意ddos 的情况下很难碰撞上.
* 固定 18个字节二进制.
* 6个字节 时间到毫秒. ( 大约 8925 年)
* 1-4个字节 递增变长编码 当前进程挂锁顺序增加id (时间变了之后,从0 开始递增) (2位表示后面有几个字节,6-30位表示后面的id 最大 2^30-1 = 1073741823)
* 8-11个字节 随机二进制数据.(来自强随机api)
* 然后base32 编码 使用不易混淆的 字符,使用从ascii码从大到小递增的字符串
*/
func NewId() string {
	ms := uint64(time.Now().UnixMilli())
	incrU32 := nextId(ms)
	return shortEncode(ms, incrU32)
}

func shortEncode(ms uint64, incrU32 uint32) string {
	var dst [29]byte
	var src [18]byte
	fillIdBytes(src[:], ms, incrU32)
	zlibEncodeB32.HgmEncodeSlice(dst[:], src[:])
	return string(dst[:])
}

// 填充18字节的id二进制数据: 6字节时间 + 1-4字节变长递增 + 8-11字节随机.
func fillIdBytes(buf []byte, ms uint64, incrU32 uint32) {
	buf[0] = byte(ms >> 40)
	buf[1] = byte(ms >> 32)
	buf[2] = byte(ms >> 24)
	buf[3] = byte(ms >> 16)
	buf[4] = byte(ms >> 8)
	buf[5] = byte(ms)
	incrLen := encodeVarLenUint32(buf[6:], incrU32)
	randOffset := 6 + incrLen
	gRf.MustReadWithLock(buf[randOffset:])
}

var gNewIncRandIdCtx struct {
	lock    sync.Mutex
	incrU32 uint32
	lastT   uint64
}

var gRf zlibRandFast.RandFast_t

func nextId(t uint64) uint32 {
	incrU32 := uint32(0)
	gNewIncRandIdCtx.lock.Lock()
	if gNewIncRandIdCtx.lastT != t {
		gNewIncRandIdCtx.incrU32 = 0
		gNewIncRandIdCtx.lastT = t
	} else {
		gNewIncRandIdCtx.incrU32++
		incrU32 = gNewIncRandIdCtx.incrU32
	}
	gNewIncRandIdCtx.lock.Unlock()
	return incrU32
}

// 编码32位无符号整数为变长字节，返回写入的总长度
// 格式：前2位表示编码长度（00, 01, 10, 11），后30位存数值
func encodeVarLenUint32(dst []byte, val uint32) int {
	val &= 0x3FFFFFFF
	switch {
	case val <= 0x3F:
		dst[0] = byte(val&0x3F) | 0x00
		return 1
	case val <= 0x3FFF:
		dst[0] = byte((val>>8)&0x3F) | 0x40
		dst[1] = byte(val)
		return 2
	case val <= 0x3FFFFF:
		dst[0] = byte((val>>16)&0x3F) | 0x80
		dst[1] = byte(val >> 8)
		dst[2] = byte(val)
		return 3
	default:
		dst[0] = byte((val>>24)&0x3F) | 0xC0
		dst[1] = byte(val >> 16)
		dst[2] = byte(val >> 8)
		dst[3] = byte(val)
		return 4
	}
}

