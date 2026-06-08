// 需要传入 Path_AbsPathClean 的 绝对路径.
// 类似 filepath.Dir 砍掉最后一截,获取前面一部分.
// 有问题返回 "/"
// 最后没有 /
import {hgmStringTrimPrefix} from "./hgmTsWeb_strings.ts";

export function hgmPath_RemoveLastSection(absPath:string):string{
    if (absPath==="/"){
        return "/"
    }
    const pos = absPath.lastIndexOf("/")
    if (pos===-1){
        return "/"
    }
    return absPath.slice(0,pos)
}

// 路径合并,结果只能是个绝对路径
export function hgmPath_Join(...pathList:string[]):string{
    if (pathList.length===0){
        return "/"
    }
    let out = "/"
    let hasWrite = false
    for (let path of pathList){
        path = hgmStringTrimPrefix(path,"/")
        if (path===""){
            continue
        }
        if (hasWrite){
            out+="/"
        }
        out+=path
        hasWrite = true
    }
    return out
}
