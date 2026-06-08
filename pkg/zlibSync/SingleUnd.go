package zlibSync

import (
	"sync"
)

// 注意: 这个东西里面的 Fn 假设是一样的.通过闭包传进去的值只有第一个有效.
type SingleUnd struct {
	locker  sync.Mutex
	Fn      func()
	bgRunFn func() // 避免每次调用都出现 alloc的解决方案。
	// 这里不可以使用 atomic.Uint32 因为 atomic 不保证前后顺序.唯一的一次不挂锁读取是读取 su.isRunning && su.hasMoreCall 状态,但是该状态 以后可能会变成 其他状态.
	isRunning   bool
	hasMoreCall bool
}

// 异步限制 fn 的最大并发为1。保证在Do调用后较低延，会执行一遍fn。
// fn第一次设置有效，后续设置都无效。(后续逻辑必须保证fn一直是同一个不能变)
// Do 调用后不会执行fn，调用者会立刻返回。
// 在 fn 的执行过程中，再调用一次Do，这次并不会执行，而是 会 排队到本次 fn 结束后，再执行一遍fn。
// 内部保证会单线程调用fn。（fn 内部不用再挂一个单线程锁了）
// fn 里面 panic 会panic 进程。
func (su *SingleUnd) Do(fn func()) {
	su.locker.Lock()
	if su.hasMoreCall {
		su.locker.Unlock()
		return
	}
	if su.isRunning && su.hasMoreCall == false {
		su.hasMoreCall = true
		su.locker.Unlock()
		return
	}
	if su.bgRunFn == nil {
		if su.Fn == nil {
			if fn == nil {
				su.locker.Unlock()
				panic(`SingleUnd su.Fn==nil`)
			}
			su.Fn = fn
		}
		su.bgRunFn = su.runBgThread
	}
	su.isRunning = true
	su.locker.Unlock()
	go su.bgRunFn()
}

func (su *SingleUnd) runBgThread() {
	su.locker.Lock()
	fn := su.Fn
	su.locker.Unlock()
	for {
		fn()
		su.locker.Lock()
		if su.hasMoreCall == false {
			su.isRunning = false
			su.locker.Unlock()
			return
		}
		su.hasMoreCall = false
		su.locker.Unlock()
	}
}

func (su *SingleUnd) IsRunning() bool {
	su.locker.Lock()
	running := su.isRunning
	su.locker.Unlock()
	return running
}

