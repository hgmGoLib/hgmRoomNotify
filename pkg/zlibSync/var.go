package zlibSync

import (
	"sync"
)

// 与 atomic.Pointer 的区别，这里直接使用 T 这个类型，默认零值。
type Var[T any] struct {
	obj  T
	lock sync.Mutex
}

func (v *Var[T]) Set(t T) {
	v.lock.Lock()
	v.obj = t
	v.lock.Unlock()
}

func (v *Var[T]) Get() T {
	v.lock.Lock()
	t := v.obj
	v.lock.Unlock()
	return t
}

// 在一个挂锁的回调里面执行逻辑，保证 *T 不是 nil。
func (v *Var[T]) LockCb(fn func(t *T)) {
	v.lock.Lock()
	defer v.lock.Unlock()
	fn(&v.obj)
}

