package webchat

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// hgmRoomNotify 开源项目根目录在 go.mod 里的 module 路径. 用它做"向上定位"的锚:
// 谁都不写死 ../ 层数, 一律从当前 .go 文件位置向上走, 找到带这一行的 go.mod 即为项目根.
const hgmRoomNotifyModulePath = "github.com/hgmGoLib/hgmRoomNotify"

// 向上定位到 hgmRoomNotify 开源项目根目录(返回绝对路径).
// 锚点是"本 .go 文件自身的位置"(runtime.Caller): 从它所在目录一路往上找, 直到某个目录的
// go.mod 里写着 `module github.com/hgmGoLib/hgmRoomNotify`. 这样不管 example/WebChat 这棵子树
// 以后怎么挪、嵌多深, 都能稳定找到根, 不依赖运行时 cwd, 也不用一堆 ../ 数层数.
func FindHgmRoomNotifyRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller 失败, 无法定位 hgmRoomNotify 项目根")
	}
	dir := filepath.Dir(thisFile)
	for {
		if isHgmRoomNotifyRoot(filepath.Join(dir, "go.mod")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("从 " + thisFile + " 向上找不到 hgmRoomNotify 项目根(带 module " + hgmRoomNotifyModulePath + " 的 go.mod)")
		}
		dir = parent
	}
}

// 判断某个 go.mod 是不是 hgmRoomNotify 项目根的 go.mod(module 行精确匹配).
func isHgmRoomNotifyRoot(goModPath string) bool {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "module "+hgmRoomNotifyModulePath {
			return true
		}
	}
	return false
}

// 编译前端(走和真人一模一样的 npm 流程: 缺 node_modules 先 npm install, 再 npm run build —— 后者的
// prebuild 钩子会把浏览器客户端 TS 复制进来, esbuild 把 React 打包成单文件), 返回打包好的 app.js 内容.
// root 用 FindHgmRoomNotifyRoot() 拿到; 前端工程路径相对 root 算, 全程不出现跨工程 ../.
func BuildFrontend(root string) ([]byte, error) {
	webDir := filepath.Join(root, "example", "WebChat", "web")
	if _, err := os.Stat(filepath.Join(webDir, "node_modules")); err != nil {
		if err := runNpm(webDir, "install", "--no-audit", "--no-fund"); err != nil {
			return nil, err
		}
	}
	if err := runNpm(webDir, "run", "build"); err != nil {
		return nil, err
	}
	appJs := filepath.Join(webDir, "dist", "app.js")
	js, err := os.ReadFile(appJs)
	if err != nil {
		return nil, fmt.Errorf("读取打包产物 %s 失败: %w", appJs, err)
	}
	if len(js) == 0 {
		return nil, fmt.Errorf("打包产物 %s 为空", appJs)
	}
	return js, nil
}

// 在指定目录里跑一条 npm 命令, 失败时把 stderr 一并带回.
func runNpm(dir string, args ...string) error {
	cmd := exec.Command("npm", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm %v (in %s) 失败: %w\n%s", args, dir, err, stderr.String())
	}
	return nil
}
