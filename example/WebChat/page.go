package main

import "fmt"

// 把打包好的前端 JS 内嵌进一个最小 HTML 页面. main() 和自动测试复用同一个模板:
// React 应用挂载到 #root; 通过 window.__webchatCfg 把房间号/用户名注入给前端.
func buildPage(bundledJs string, roomId string, sender string) string {
	return fmt.Sprintf(`<!doctype html>
<html>
<head><meta charset="utf-8"><title>hgmRoomNotify WebChat</title></head>
<body>
<div id="root"></div>
<script>window.__webchatCfg = {roomId: %q, sender: %q};</script>
<script>%s</script>
</body>
</html>`, roomId, sender, bundledJs)
}
