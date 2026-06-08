package zlibTimer

import (
	"sync"
	"time"
)

// Timer 是线程安全的 time.AfterFunc 封装，支持零值内嵌在 struct 中使用。
// 使用方式：调用 SetFn 设置回调，调用 After 设置触发时间，两者都设置后 timer 自动启动。
// SetFn 和 After 的调用顺序无关。
// After(0) 与 Go 标准库语义一致：立即触发（dur<=0 均立即触发）。停止请用 Stop()。
// 所有 API 均不会 panic。
type Timer struct {
	lock          sync.Mutex
	t             *time.Timer
	fn            func()
	afterTime     time.Time
	durSetNoStart bool
}

// SetFn 设置回调函数。只有第一次调用有效，后续调用忽略。
// 若 After 已经调用过，则用暂存的 dur 启动 timer（dur<=0 也会立即触发）。
func (t *Timer) SetFn(fn func()) {
	t.lock.Lock()
	defer t.lock.Unlock()
	if t.fn != nil {
		return
	}
	t.fn = fn
	if t.durSetNoStart {
		dur2 := time.Until(t.afterTime)
		t.t = time.AfterFunc(dur2, t.fn)
		t.durSetNoStart = false
		t.afterTime = time.Time{}
	}
}

// After 设置触发时间。
// timer 未启动时暂存 dur 等待 SetFn；timer 已启动时重置触发时间。
// dur<=0 与 Go 标准库语义一致，表示立即触发。停止请用 Stop()。
// timer 已触发后再调用 After，会重新启动 timer。
func (t *Timer) After(dur time.Duration) {
	t.lock.Lock()
	defer t.lock.Unlock()
	if t.t == nil {
		if t.fn == nil {
			t.afterTime = time.Now().Add(dur)
			t.durSetNoStart = true
			return
		}
		t.t = time.AfterFunc(dur, t.fn)
	} else {
		t.t.Reset(dur)
	}
}

// Stop 停止 timer。未启动时调用安全，不会 panic。
func (t *Timer) Stop() {
	t.lock.Lock()
	defer t.lock.Unlock()
	if t.t != nil {
		t.t.Stop()
	}
}

