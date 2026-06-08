export async function asyncSleep(dur:number){
    await new Promise<void>(resolve => setTimeout(()=>{
        resolve()
    },dur))
}
