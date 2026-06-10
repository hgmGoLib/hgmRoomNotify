// WebChat 例子的可运行入口: 起后端 + 把打包好的前端页面发出去, 浏览器打开即可聊天.
//
// 启动步骤:
//  1. cd hgmRoomNotify/example/WebChat/web && npm install && npm run build   # 打包 React 前端到 web/dist/app.js
//  2. cd hgmRoomNotify/example && go run ./WebChat                            # 起后端(默认 127.0.0.1:8080)
//  3. 浏览器打开 http://127.0.0.1:8080 , 多开几个标签页即可互相聊天.
//
// 真机自动测试(无需先 npm build, 测试内部会自动打包)见 webchat_test.go:
//  cd hgmRoomNotify/example && go test ./WebChat
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

func main() {
	const roomId = "chat:room1"

	// 读取 npm 打包好的前端 JS. 没打包时给出明确提示, 而不是发一个空白页.
	bundledJs, err := os.ReadFile(filepath.Join(webChatDir(), "web", "dist", "app.js"))
	if err != nil {
		log.Fatalf("读不到前端打包产物 web/dist/app.js: %v\n请先执行: cd hgmRoomNotify/example/WebChat/web && npm install && npm run build", err)
	}
	page := buildPage(string(bundledJs), roomId, "")

	store := &chatStore_t{}
	cs := newChatServer(store)

	addr := "127.0.0.1:8080"
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", addr, err)
	}
	fmt.Printf("WebChat 已启动: http://%s  (房间 %s, 多开标签页互相聊天)\n", ln.Addr().String(), roomId)
	if err := http.Serve(ln, cs.routes(page)); err != nil {
		log.Fatal(err)
	}
}

// 本源文件所在的 WebChat 目录绝对路径(不依赖运行时 cwd).
func webChatDir() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		log.Fatal("runtime.Caller 失败, 无法定位 WebChat 目录")
	}
	return filepath.Dir(thisFile)
}
