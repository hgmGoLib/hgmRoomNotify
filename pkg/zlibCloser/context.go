package zlibCloser

import (
	"context"
)

func (c *Closer) GetCloseContext() context.Context {
	if c == nil {
		ctx2, cancelFunc := context.WithCancel(context.Background())
		cancelFunc()
		return ctx2
	}
	fnId := gFnId.Add(1)
	c.lock.Lock()
	var ctx2 context.Context
	var cancelFunc func()
	if c.cacheContext == nil {
		ctx2, cancelFunc = context.WithCancel(context.Background())
		c.cacheContext = ctx2
		if c.isClosed.Load() {
			cancelFunc()
		}
		c.closeFnList = append(c.closeFnList, closeFn_t{
			i:  fnId,
			fn: cancelFunc,
		})
	} else {
		ctx2 = c.cacheContext
	}
	c.lock.Unlock()
	return ctx2
}

// 用自己的 context 开个 context
// 关闭这个子级 不关闭 close.
func (c *Closer) NewContextWithCancel() (ctx context.Context, cancelFunc context.CancelFunc) {
	return context.WithCancel(c.GetCloseContext())
}

