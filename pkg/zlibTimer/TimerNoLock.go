package zlibTimer

import (
	"time"
)

// Timer 的无锁版本，接口完全一致。
// 调用者需要自行保证初始化阶段的时序安全：
// 先在单 goroutine 中完成 SetFn，并在首次 After 后让内部 *time.Timer 建立起来。
// 在这之后，如果只并发调用 After/Stop，或者在 AfterFunc 回调自身中再次调用 After，
// 这种用法在当前实现下是有效的。
// 但不要并发执行 SetFn，也不要在内部 *time.Timer 尚未建立前并发调用 After/Stop。
// 适用于调用方已经有自己的锁管理，或者已经明确区分初始化阶段和运行阶段的场景。
type TimerNoLock struct {
	t             *time.Timer
	fn            func()
	afterTime     time.Time
	durSetNoStart bool
}

// 设置回调函数。只有第一次调用有效，后续调用忽略。
// 若 After 已经调用过，则用暂存的 dur 启动 timer（dur<=0 也会立即触发）。
func (t *TimerNoLock) SetFn(fn func()) {
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

// 设置触发时间。
// timer 未启动时暂存 dur 等待 SetFn；timer 已启动时重置触发时间。
// dur<=0 与 Go 标准库语义一致，表示立即触发。停止请用 Stop()。
// timer 已触发后再调用 After，会重新启动 timer。
func (t *TimerNoLock) After(dur time.Duration) {
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

// 停止 timer。未启动时调用安全，不会 panic。
func (t *TimerNoLock) Stop() {
	if t.t != nil {
		t.t.Stop()
	}
}

