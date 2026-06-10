package webchat

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// 本文件是 webchat_test.go 用到的测试工具的"开源自带简化版":
// 原本依赖私有 hgmLib(hgmChromeDp/hgmTest/hgmTestTimeout), 开源仓库里不能依赖,
// 所以把这三样按本测试实际用到的最小功能内联到这里, 只依赖第三方 github.com/chromedp/chromedp.

// 进程内无头浏览器自动化上下文. 封装 chromedp 的 ExecAllocator + Context.
// 浏览器二进制由 chromedp 默认探测(本机装的 Chrome/Edge/Chromium).
type zlibChromeCtx_t struct {
	ctx         context.Context
	ctxCancel   context.CancelFunc
	allocCancel context.CancelFunc
}

func newZlibChromeCtx() *zlibChromeCtx_t {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, ctxCancel := chromedp.NewContext(allocCtx)
	return &zlibChromeCtx_t{ctx: ctx, ctxCancel: ctxCancel, allocCancel: allocCancel}
}

func (c *zlibChromeCtx_t) Close() {
	c.ctxCancel()
	c.allocCancel()
}

func (c *zlibChromeCtx_t) MustNavigate(url string) {
	if err := chromedp.Run(c.ctx, chromedp.Navigate(url)); err != nil {
		panic(err)
	}
}

func (c *zlibChromeCtx_t) MustWaitBodyReady() {
	if err := chromedp.Run(c.ctx, chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
		panic(err)
	}
}

func (c *zlibChromeCtx_t) MustEvalJsNoReturn(js string) {
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(js, nil)); err != nil {
		panic(err)
	}
}

// 执行 js 并把返回值反序列化为 golang string. 返回值不是字符串则 panic.
func (c *zlibChromeCtx_t) MustEvalJsReturnString(js string) string {
	var out string
	if err := json.Unmarshal(c.mustEvalJsToJson(js), &out); err != nil {
		panic(err)
	}
	return out
}

// 执行 js 并把返回值反序列化为 golang float64. 返回值不是数字则 panic.
func (c *zlibChromeCtx_t) MustEvalJsReturnFloat64(js string) float64 {
	var out float64
	if err := json.Unmarshal(c.mustEvalJsToJson(js), &out); err != nil {
		panic(err)
	}
	return out
}

func (c *zlibChromeCtx_t) mustEvalJsToJson(js string) json.RawMessage {
	resultB := []byte{}
	if err := chromedp.Run(c.ctx, chromedp.Evaluate(js, &resultB)); err != nil {
		panic(err)
	}
	return json.RawMessage(resultB)
}

// 测试看门狗: 超时后 dump 全部 goroutine 栈并 panic, 防止真机测试卡死时整个 go test 永久挂起.
// 返回的 stop 在测试正常结束时调用以取消看门狗.
func zlibTestTimeout(d time.Duration) (stop func()) {
	t := time.AfterFunc(d, func() {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		panic("zlibTestTimeout fired, all goroutine stack:\n" + string(buf[:n]))
	})
	return func() { t.Stop() }
}

// 断言 got 与 want 深度相等, 不等则 t.Fatal.
func zlibEqual(t *testing.T, got any, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zlibEqual fail:\n got: %#v\nwant: %#v", got, want)
	}
}
