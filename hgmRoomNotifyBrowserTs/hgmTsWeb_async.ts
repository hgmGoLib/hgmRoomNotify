export async function hgmAsyncSleep(dur:number){
    await new Promise<void>(resolve => setTimeout(()=>{
        resolve()
    },dur))
}
