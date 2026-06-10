package webchat

import (
	"net/http/httptest"
	"testing"
	"time"
)

// 真机自动测试(正常路径): 沿用 hgmRoomNotifyTest/hgmRoomNotifyBrowserTest 的模式 ——
// 用 esbuild 把 React 前端(含 hgmRoomNotify 浏览器客户端)打包成单文件 JS, 起真后端(真 http+ws+ajax),
// 用真 Chrome(zlib_test.go 里的简化 chromedp 封装)加载页面, 验证浏览器<->golang 的可靠聊天闭环走通:
//  1. 浏览器加载页面 -> ws 连接成功并进房间.
//  2. 服务端发一条消息(模拟另一个用户) -> 浏览器经 ws 快路径收到并渲染.
//  3. 在浏览器里真实地输入并点"发送" -> 经 /chat/post 入库 + ws 回推 -> 浏览器渲染自己发的消息.
//  4. 浏览器渲染出的消息与后端内存库完全一致.
// 异常路径(断线/超大消息/服务器重启)本次不测.
func TestWebChat_NormalPath(t *testing.T) {
	stopTimeout := zlibTestTimeout(time.Second * 90)
	defer stopTimeout()

	const roomId = "chat:room1"
	// 走和真人/一键入口一模一样的编译流程: 向上定位项目根 -> 相对它 npm 打包前端.
	js, err := BuildFrontend(FindHgmRoomNotifyRoot())
	if err != nil {
		t.Fatal(err)
	}

	store := &ChatStore_t{}
	cs := NewChatServer(store)
	httpServer := httptest.NewServer(cs.Routes(BuildPage(string(js), roomId, "alice")))
	defer httpServer.Close()

	ctx := newChromeCtx()
	defer ctx.Close()
	ctx.MustNavigate(httpServer.URL)
	ctx.MustWaitBodyReady()

	// 1. 等浏览器 ws 连接成功并进房间.
	waitBrowser(t, ctx, `window.__webchatClient && window.__webchatClient.IsConnectedSucc() && window.__webchatClient.GetRoomCount() > 0`,
		"浏览器连接并进房间")
	// 空房间, 进房回放后消息列表为空.
	zlibEqual(t, evalF(ctx, `document.querySelectorAll('#msgList li.msg').length`), float64(0))

	// 2. 服务端发一条消息(模拟另一个用户 bob), 浏览器应经 ws 快路径收到.
	cs.Post(roomId, "bob", "hello world")
	waitBrowser(t, ctx, `document.querySelectorAll('#msgList li.msg').length >= 1`, "浏览器收到服务端消息")
	zlibEqual(t, evalStr(ctx, `document.querySelector('#msgList li.msg[data-idx="1"]').textContent`), "bob: hello world")

	// 3. 在浏览器里真实输入并点击发送(alice). 经 /chat/post 入库 + ws 回推 -> 浏览器渲染.
	ctx.MustEvalJsNoReturn(`(function(){
		var inp = document.getElementById('msgInput');
		var setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
		setter.call(inp, 'hi from alice');
		inp.dispatchEvent(new Event('input', {bubbles:true}));
		document.getElementById('sendBtn').click();
	})()`)
	waitBrowser(t, ctx, `document.querySelectorAll('#msgList li.msg').length >= 2`, "浏览器渲染自己发的消息")
	zlibEqual(t, evalStr(ctx, `document.querySelector('#msgList li.msg[data-idx="2"]').textContent`), "alice: hi from alice")

	// 4. 浏览器渲染的消息与后端内存库完全一致.
	want := store.After(roomId, 0)
	zlibEqual(t, len(want), 2)
	zlibEqual(t, want[0].Sender+": "+want[0].Text, "bob: hello world")
	zlibEqual(t, want[1].Sender+": "+want[1].Text, "alice: hi from alice")
	// 全程连接保持.
	zlibEqual(t, evalBool(ctx, `window.__webchatClient.IsConnectedSucc()`), float64(1))
}

func newChromeCtx() *zlibChromeCtx_t {
	return newZlibChromeCtx()
}

// 轮询浏览器里某个 JS 布尔表达式直到为真, 超时 t.Fatal.
func waitBrowser(t *testing.T, ctx *zlibChromeCtx_t, jsCond string, msg string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if evalBool(ctx, jsCond) == 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("waitBrowser timeout: %s", msg)
}

func evalF(ctx *zlibChromeCtx_t, js string) float64 {
	return ctx.MustEvalJsReturnFloat64(js)
}

func evalBool(ctx *zlibChromeCtx_t, js string) float64 {
	return ctx.MustEvalJsReturnFloat64(js + " ? 1 : 0")
}

func evalStr(ctx *zlibChromeCtx_t, js string) string {
	return ctx.MustEvalJsReturnString(js)
}
