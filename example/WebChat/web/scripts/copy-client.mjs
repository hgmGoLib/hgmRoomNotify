// 编译前把 hgmRoomNotify 浏览器客户端(TypeScript)覆盖复制进 web/src/hgmRoomNotifyBrowserTs/.
// 这样应用源码用本地 import("./hgmRoomNotifyBrowserTs/index.ts")即可, 不需要 ../ 爬出 web 工程;
// 复制品已 gitignore, 仓库里不留第二份 TS 代码.
// 由 npm 的 prebuild/predev 钩子自动执行(见 package.json).
import {cpSync, rmSync, existsSync} from "node:fs"
import {fileURLToPath} from "node:url"
import {dirname, resolve} from "node:path"

const here = dirname(fileURLToPath(import.meta.url)) // web/scripts
const src = resolve(here, "../../../../hgmRoomNotifyBrowserTs") // hgmRoomNotify/hgmRoomNotifyBrowserTs
const dst = resolve(here, "../src/hgmRoomNotifyBrowserTs")

if (!existsSync(src)) {
    console.error("找不到 hgmRoomNotify 浏览器客户端源码:", src)
    process.exit(1)
}
rmSync(dst, {recursive: true, force: true})
cpSync(src, dst, {recursive: true})
console.log("copied hgmRoomNotifyBrowserTs ->", dst)
