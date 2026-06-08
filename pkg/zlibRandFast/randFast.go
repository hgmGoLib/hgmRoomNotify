package zlibRandFast

import (
	"crypto/rand"
	"sync"
)

const randFastOneSize = 512

// 减少对于 rand.Read 的调用次数，提高获取随机数据的速度。安全性未知。
// 注意只能单线程调用.多线程调用请挂锁.
type RandFast_t struct {
	buf    [randFastOneSize]byte
	remain uint16
	lock   sync.Mutex
}

// 需要单线程调用,不可以并发调用
func (r *RandFast_t) Read(buf []byte) (err error) {
	if len(buf) == 0 {
		return nil
	}
	for {
		if r.remain == 0 {
			_, err = rand.Read(r.buf[:])
			if err != nil {
				return err
			}
			r.remain = uint16(len(r.buf))
		}
		thisSize := r.remain
		if int(thisSize) > len(buf) {
			thisSize = uint16(len(buf))
		}
		copy(buf[:thisSize], r.buf[randFastOneSize-r.remain:randFastOneSize-r.remain+thisSize])
		r.remain -= thisSize
		buf = buf[thisSize:]
		if len(buf) == 0 {
			return nil
		}
	}
}

func (r *RandFast_t) MustReadWithLock(buf []byte) {
	r.lock.Lock()
	err := r.Read(buf)
	r.lock.Unlock()
	if err != nil {
		panic(err)
	}
}

