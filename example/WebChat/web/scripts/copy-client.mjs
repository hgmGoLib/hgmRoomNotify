// 编译前把 hgmRoomNotify 浏览器客户端(TypeScript)覆盖复制进 web/src/hgmRoomNotifyBrowserTs/.
// 这样应用源码用本地 import("./hgmRoomNotifyBrowserTs/index.ts")即可, 不需要 ../ 爬出 web 工程;
// 复制品已 gitignore, 仓库里不留第二份 TS 代码.
// 由 npm 的 prebuild/predev 钩子自动执行(见 package.json).
import {cpSync, rmSync, existsSync, readFileSync} from "node:fs"
import {fileURLToPath} from "node:url"
import {dirname, resolve, join} from "node:path"

const here = dirname(fileURLToPath(import.meta.url)) // web/scripts

// 和 Go 侧 FindHgmRoomNotifyRoot 用同一个锚: 从本脚本位置向上走, 找到带
// `module github.com/hgmGoLib/hgmRoomNotify` 的 go.mod 即为开源项目根. 不写死 ../ 层数,
// example/WebChat 子树以后怎么挪都不用改这里.
function findHgmRoomNotifyRoot(start) {
    let dir = start
    for (;;) {
        const goMod = join(dir, "go.mod")
        if (existsSync(goMod) &&
            readFileSync(goMod, "utf8").split("\n").some((l) => l.trim() === "module github.com/hgmGoLib/hgmRoomNotify")) {
            return dir
        }
        const parent = dirname(dir)
        if (parent === dir) {
            throw new Error("向上找不到 hgmRoomNotify 项目根(带 module github.com/hgmGoLib/hgmRoomNotify 的 go.mod)")
        }
        dir = parent
    }
}

const root = findHgmRoomNotifyRoot(here)
const src = join(root, "hgmRoomNotifyBrowserTs")
const dst = resolve(here, "../src/hgmRoomNotifyBrowserTs")

if (!existsSync(src)) {
    console.error("找不到 hgmRoomNotify 浏览器客户端源码:", src)
    process.exit(1)
}
rmSync(dst, {recursive: true, force: true})
cpSync(src, dst, {recursive: true})
console.log("copied hgmRoomNotifyBrowserTs ->", dst)
