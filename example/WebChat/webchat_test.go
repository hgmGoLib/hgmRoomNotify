package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"v12w.x34y.com/bronze1man/hgmLib/hgmChromeDp"
	"v12w.x34y.com/bronze1man/hgmLib/hgmFilePath/hgmFpGowork"
	"v12w.x34y.com/bronze1man/hgmLib/hgmTest"
	"v12w.x34y.com/bronze1man/hgmLib/hgmTest/hgmTestTimeout"
)

// 真机自动测试(正常路径): 沿用 hgmRoomNotifyTest/hgmRoomNotifyBrowserTest 的模式 ——
// 用 esbuild 把 React 前端(含 hgmRoomNotify 浏览器客户端)打包成单文件 JS, 起真后端(真 http+ws+ajax),
// 用真 Chrome(hgmChromeDp)加载页面, 验证浏览器<->golang 的可靠聊天闭环走通:
//  1. 浏览器加载页面 -> ws 连接成功并进房间.
//  2. 服务端发一条消息(模拟另一个用户) -> 浏览器经 ws 快路径收到并渲染.
//  3. 在浏览器里真实地输入并点"发送" -> 经 /chat/post 入库 + ws 回推 -> 浏览器渲染自己发的消息.
//  4. 浏览器渲染出的消息与后端内存库完全一致.
// 异常路径(断线/超大消息/服务器重启)本次不测.
func TestWebChat_NormalPath(t *testing.T) {
	hgmTestTimeout.Set(time.Second * 90)
	defer hgmTestTimeout.Stop()

	const roomId = "chat:room1"
	js := buildWebChatJs(t)

	store := &chatStore_t{}
	cs := newChatServer(store)
	httpServer := httptest.NewServer(cs.routes(buildPage(js, roomId, "alice")))
	defer httpServer.Close()

	ctx := newChromeCtx()
	defer ctx.Close()
	ctx.MustNavigate(httpServer.URL)
	ctx.MustWaitBodyReady()

	// 1. 等浏览器 ws 连接成功并进房间.
	waitBrowser(t, ctx, `window.__webchatClient && window.__webchatClient.IsConnectedSucc() && window.__webchatClient.GetRoomCount() > 0`,
		"浏览器连接并进房间")
	// 空房间, 进房回放后消息列表为空.
	hgmTest.Equal(evalF(ctx, `document.querySelectorAll('#msgList li.msg').length`), float64(0))

	// 2. 服务端发一条消息(模拟另一个用户 bob), 浏览器应经 ws 快路径收到.
	cs.Post(roomId, "bob", "hello world")
	waitBrowser(t, ctx, `document.querySelectorAll('#msgList li.msg').length >= 1`, "浏览器收到服务端消息")
	hgmTest.Equal(evalStr(ctx, `document.querySelector('#msgList li.msg[data-idx="1"]').textContent`), "bob: hello world")

	// 3. 在浏览器里真实输入并点击发送(alice). 经 /chat/post 入库 + ws 回推 -> 浏览器渲染.
	ctx.MustEvalJsNoReturn(`(function(){
		var inp = document.getElementById('msgInput');
		var setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
		setter.call(inp, 'hi from alice');
		inp.dispatchEvent(new Event('input', {bubbles:true}));
		document.getElementById('sendBtn').click();
	})()`)
	waitBrowser(t, ctx, `document.querySelectorAll('#msgList li.msg').length >= 2`, "浏览器渲染自己发的消息")
	hgmTest.Equal(evalStr(ctx, `document.querySelector('#msgList li.msg[data-idx="2"]').textContent`), "alice: hi from alice")

	// 4. 浏览器渲染的消息与后端内存库完全一致.
	want := store.After(roomId, 0)
	hgmTest.Equal(len(want), 2)
	hgmTest.Equal(want[0].Sender+": "+want[0].Text, "bob: hello world")
	hgmTest.Equal(want[1].Sender+": "+want[1].Text, "alice: hi from alice")
	// 全程连接保持.
	hgmTest.Equal(evalBool(ctx, `window.__webchatClient.IsConnectedSucc()`), float64(1))
}

// 走和用户启动步骤一样的 `npm run build`: prebuild 钩子先把 hgmRoomNotifyBrowserTs 复制进 web/src,
// 再 esbuild 打包 React 前端到 web/dist/app.js, 读出来返回. node_modules 不在时先 npm install.
func buildWebChatJs(t *testing.T) string {
	webDir := hgmFpGowork.MustPathInGoWork("hgmRoomNotify/example/WebChat/web")
	if _, err := os.Stat(filepath.Join(webDir, "node_modules")); err != nil {
		runNpm(t, webDir, "install", "--no-audit", "--no-fund")
	}
	runNpm(t, webDir, "run", "build")
	js, err := os.ReadFile(filepath.Join(webDir, "dist", "app.js"))
	if err != nil {
		t.Fatalf("读取打包产物 dist/app.js 失败: %v", err)
	}
	if len(js) == 0 {
		t.Fatal("打包产物为空")
	}
	return string(js)
}

func runNpm(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("npm", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("npm %v 失败: %v\n%s", args, err, stderr.String())
	}
}

func newChromeCtx() *hgmChromeDp.ChromeDpCtx {
	return hgmChromeDp.NewChromeDpCtx(hgmChromeDp.NewChromeDpCtxReq{})
}

// 轮询浏览器里某个 JS 布尔表达式直到为真, 超时 t.Fatal.
func waitBrowser(t *testing.T, ctx *hgmChromeDp.ChromeDpCtx, jsCond string, msg string) {
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

func evalF(ctx *hgmChromeDp.ChromeDpCtx, js string) float64 {
	return ctx.MustEvalJsReturnFloat64(js)
}

func evalBool(ctx *hgmChromeDp.ChromeDpCtx, js string) float64 {
	return ctx.MustEvalJsReturnFloat64(js + " ? 1 : 0")
}

func evalStr(ctx *hgmChromeDp.ChromeDpCtx, js string) string {
	return ctx.MustEvalJsReturnString(js)
}
