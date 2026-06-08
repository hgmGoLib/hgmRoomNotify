export async function hgmRn_asyncSleep(dur:number){
    await new Promise<void>(resolve => setTimeout(()=>{
        resolve()
    },dur))
}
