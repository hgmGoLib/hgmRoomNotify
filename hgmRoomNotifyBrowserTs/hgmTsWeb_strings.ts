export function hgmStringTrimPrefix(s:string,prefix:string):string{
    if (s.startsWith(prefix)===false){
        return s
    }
    return s.slice(prefix.length)
}
