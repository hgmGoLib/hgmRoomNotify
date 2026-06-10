// WebChat 例子的一键入口: 不用先手动 npm、不用关心在哪个目录跑.
//
//	cd hgmRoomNotify/example && go run ./WebChat/WebChatRun
//	# 浏览器打开 http://127.0.0.1:8080 , 多开几个标签页互相聊天.
//
// 它干的事(全部相对"开源项目根"定位, 不写死 ../ 层数):
//  1. 向上定位到 hgmRoomNotify 项目根(webchat.FindHgmRoomNotifyRoot, 用 go.mod 做锚).
//  2. 编译前端(webchat.BuildFrontend: 缺依赖先 npm install, 再 npm run build 打包 React).
//  3. 起后端, 把打包好的页面发出去.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"

	webchat "github.com/hgmGoLib/hgmRoomNotifyExample/WebChat"
)

func main() {
	const roomId = "chat:room1"
	const addr = "127.0.0.1:8080"

	// 1. 定位项目根. 2. 相对它编译前端, 拿到打包好的 app.js.
	root := webchat.FindHgmRoomNotifyRoot()
	log.Printf("hgmRoomNotify 项目根: %s", root)
	log.Printf("编译前端中(首次会 npm install, 稍等)...")
	bundledJs, err := webchat.BuildFrontend(root)
	if err != nil {
		log.Fatalf("编译前端失败: %v", err)
	}

	// 3. 起后端: 内存库 + ws 戳一下层 + 把内嵌前端 JS 的页面发出去.
	page := webchat.BuildPage(string(bundledJs), roomId, "")
	cs := webchat.NewChatServer(&webchat.ChatStore_t{})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("监听 %s 失败: %v", addr, err)
	}
	fmt.Printf("WebChat 已启动: http://%s  (房间 %s, 多开标签页互相聊天)\n", ln.Addr().String(), roomId)
	if err := http.Serve(ln, cs.Routes(page)); err != nil {
		log.Fatal(err)
	}
}
