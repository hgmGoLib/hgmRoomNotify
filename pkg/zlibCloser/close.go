package zlibCloser

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// 一个关闭对象。
// 空值表示没关闭。不用调用函数初始化。
// 只能关闭一次的关闭对象。第二次调用关闭不报错,依然处于关闭状态。
// 支持功能：
//   - 查询是否关闭
//   - 当前线程等待到关闭。
//   - 返回关闭时关闭的channel。
//   - 返回关闭时关闭的context。
//   - 注册在关闭时执行的动作的回调。
type Closer struct {
	//isClosed        bool
	isClosed     atomic.Bool
	closeChan    chan struct{}
	cacheContext context.Context
	closeFnList  []closeFn_t
	lock         sync.Mutex
}

type closeFn_t struct {
	i  uint64
	fn func()
}

// nil 对象 等于关闭。
func (c *Closer) IsClose() bool {
	if c == nil {
		return true
	}
	return c.isClosed.Load()
}

// 该函数一定返回 nil。（返回值仅用于符合 interface 接口，无实际价值）
// 第一次调用会同步执行所有注册的回调，第二次调用什么也不做。（注意调用一开始就标记当前对象关闭，一旦关闭，后续调用就什么都不做。
func (c *Closer) Close() error {

	c.lock.Lock()
	if c.isClosed.Load() {
		c.lock.Unlock()
		return nil
	}
	c.isClosed.Store(true)
	if c.closeChan != nil {
		close(c.closeChan)
	}
	fnList := c.closeFnList
	c.closeFnList = nil
	c.lock.Unlock()
	for i := len(fnList) - 1; i >= 0; i-- {
		fnList[i].fn()
	}
	return nil
}

func (c *Closer) Close2() {
	c.Close()
}

// 注册在 关闭时的回调。
// 如果当前已经关闭了，则直接调用。
// 无panic保护 回调不要panic
// 回调可能会与 Close 并发执行(大概率是在 Close里面执行)
func (c *Closer) AddOnClose(fn func()) {
	fnId := gFnId.Add(1)
	c.lock.Lock()
	if c.isClosed.Load() == false {
		c.closeFnList = append(c.closeFnList, closeFn_t{
			i:  fnId,
			fn: fn,
		})
		c.lock.Unlock()
		return
	}
	c.lock.Unlock()
	fn()
}

// 注册在 关闭时的回调，对象形式。
func (c *Closer) AddOnCloser(closer io.Closer) {
	c.AddOnClose(func() {
		closer.Close()
	})
}

// 返回 true 表示 时间到了。返回 false 表示 closer 关掉了
func (c *Closer) Sleep(Dur time.Duration) bool {
	if c.IsClose() {
		return false
	}
	timer := time.NewTimer(Dur)
	select {
	case <-timer.C:
		return true
	case <-c.GetCloseChan():
		timer.Stop()
		return false
	}
}

func (c *Closer) GetCloseChan() <-chan struct{} {
	c.lock.Lock()
	if c.closeChan == nil {
		c.closeChan = make(chan struct{})
		if c.isClosed.Load() {
			close(c.closeChan)
		}
	}
	thisChan := c.closeChan
	c.lock.Unlock()
	return thisChan
}

func (c *Closer) WaitClose() {
	c2 := c.GetCloseChan()
	<-c2
}

// 注册在 关闭时的回调。
// 如果当前已经关闭了，则直接调用。
// 返回一个函数,调用了可以把刚才的注册删掉. (如果没有这个需求,可以调用 AddOnClose) 如果已经close了,并且回调已经调用了,则什么都不会发生.
// 无panic保护 回调不要panic
// 回调可能会与 Close 并发执行(大概率是在 Close里面执行)
func (c *Closer) AddOnClose2(fn func()) (deleteFn func()) {
	fnId := gFnId.Add(1)
	c.lock.Lock()
	if c.isClosed.Load() == false {
		c.closeFnList = append(c.closeFnList, closeFn_t{
			i:  fnId,
			fn: fn,
		})
		c.lock.Unlock()
		return func() {
			c.lock.Lock()
			for i, fnObj := range c.closeFnList {
				if fnObj.i == fnId {
					c.closeFnList[i] = c.closeFnList[len(c.closeFnList)-1]
					c.closeFnList = c.closeFnList[:len(c.closeFnList)-1]
					c.lock.Unlock()
					return
				}
			}
			c.lock.Unlock()
		}
	}
	c.lock.Unlock()
	fn()

	return func() {}
}

var gFnId = atomic.Uint64{}

