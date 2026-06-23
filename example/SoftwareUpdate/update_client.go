package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hgmGoLib/hgmRoomNotify"
)

// 更新客户端: 包一层 hgmRoomNotify.Client.
//
// 正确性保底完全靠 check api, 不依赖 ws:
//   - 进程启动 startupDelay 后无条件 check 一次(即使 ws 被封/连不上也照跑)。
//   - 之后每 pollInterval 再 check 一次(低频保底)。
// 只要进程在跑, 哪怕 ws 全程不通, 也能靠这两步把更新拿到(顶多晚一个 pollInterval)。
// ws + CVersionId 只是【加速实时性】: 服务端一发布就戳一下, 让客户端不必等到下个 poll 周期就回源 check。
// 它命不命中、丢不丢, 都不影响最终能否更新 —— 软件更新坏了很恶心, 所以正确性绝不押在不可靠的 ws 上。
type updateClient_t struct {
	httpBase       string
	wsUrl          string
	currentVersion string        // 本进程当前运行的版本.
	startupDelay   time.Duration // 进程启动后多久做第一次 check. 生产规范 10s, 测试调小.
	pollInterval   time.Duration // 保底周期 check 间隔. 生产可设几小时, <=0 表示关闭周期保底.

	client hgmRoomNotify.Client

	lock      sync.Mutex
	leaveFn   func()
	lastEpoch string         // 已知房间纪元. 纪元变了一律回源(不能跳过).
	pending   *releaseInfo_t // 查到的、待安装的新版本(nil 表示当前已是最新).

	stopCh   chan struct{}
	stopOnce sync.Once

	checkCount atomic.Uint64 // 回源调用 check api 的次数(可靠路径). 演示/断言用.
	skipCount  atomic.Uint64 // 靠 CVersionId 跳过回源的次数(加速命中). 演示/断言用.
}

func newUpdateClient(httpBase string, wsUrl string, currentVersion string, startupDelay time.Duration, pollInterval time.Duration) *updateClient_t {
	uc := &updateClient_t{
		httpBase:       httpBase,
		wsUrl:          wsUrl,
		currentVersion: currentVersion,
		startupDelay:   startupDelay,
		pollInterval:   pollInterval,
		stopCh:         make(chan struct{}),
	}
	uc.client.SetWsDialUrl(wsUrl)
	return uc
}

// 启动客户端: 起一个 goroutine, 等 startupDelay 后做第一次 check(保底, 不碰 ws); 若已是最新,
// 再开 ws 实时监听(加速)+ 周期保底 check(即使 ws 全程不通也能更新).
func (uc *updateClient_t) Start() {
	go func() {
		select {
		case <-uc.stopCh:
			return
		case <-time.After(uc.startupDelay):
		}
		// 1. 启动后第一次, 走【可靠数据源】问服务端: 我这个版本要不要更新? 这一步只用 http, 跟 ws 通不通无关.
		need, _ := uc.doCheckAndMaybeApply()
		if need {
			// 需要更新: 已记录待安装版本+下载地址(真实场景: 下载并重启到新版本), 不再监听.
			return
		}
		// 2. 已是最新: 开 ws 实时监听(加速)+ 周期保底 check(正确性靠它, 不靠 ws).
		fn := uc.client.RoomEnter(updateRoomId, uc.onChange)
		uc.lock.Lock()
		uc.leaveFn = fn
		uc.lock.Unlock()
		go uc.pollLoop()
	}()
}

// 周期保底: 每 pollInterval 回源 check 一次, 不依赖 ws. 这是"ws 被封也能更新"的正确性来源.
// 查到更新或被 Close 就停. check 失败不要紧, 下个周期会再试.
func (uc *updateClient_t) pollLoop() {
	if uc.pollInterval <= 0 {
		return
	}
	t := time.NewTicker(uc.pollInterval)
	defer t.Stop()
	for {
		select {
		case <-uc.stopCh:
			return
		case <-t.C:
			if uc.Pending() != nil {
				return // 已查到更新, 不必再轮询.
			}
			_, _ = uc.doCheckAndMaybeApply()
		}
	}
}

// 房间变更回调: CVersionId 加速路径. 它只决定"要不要提前回源一次", 不决定正确性 —— 跳过了也有周期保底兜着.
// CVersionId 带着"当前最新版本号"(加速字段, 不保证有). 只有在【非空 + 可比 + 同纪元 + 不比我新】时才跳过这次回源;
// 其余一切(空 / 不可比 / 纪元变 / 比我新)都回源问 check api.
func (uc *updateClient_t) onChange(ev *hgmRoomNotify.RoomOnChange_t) {
	uc.lock.Lock()
	sameEpoch := ev.RoomEpoch == uc.lastEpoch
	uc.lastEpoch = ev.RoomEpoch
	uc.lock.Unlock()

	if ev.CVersionId != "" && sameEpoch {
		if sign, ok := compareVersion(ev.CVersionId, uc.currentVersion); ok && sign <= 0 {
			// 加速命中: 推来的最新版本不比我现在的新 -> 这次只是重连回放/重复戳, 跳过回源(周期保底仍兜着正确性).
			uc.skipCount.Add(1)
			return
		}
	}
	// 回源问 check api 拿权威结果 + 下载地址.
	if _, err := uc.doCheckAndMaybeApply(); err != nil {
		// 回源失败: 设 ErrMsg 让客户端进入 needManual; 不要紧, 周期保底下次还会再 check.
		ev.ErrMsg = "更新检查失败: " + err.Error()
	}
}

// 回源: 调 check api. 要更新就记录待安装版本(真实场景: 下载 DownloadURL 并重启). 返回 (是否需要更新, err).
func (uc *updateClient_t) doCheckAndMaybeApply() (bool, error) {
	uc.checkCount.Add(1)
	u := uc.httpBase + "/update/check?version=" + url.QueryEscape(uc.currentVersion)
	resp, err := http.Get(u)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out checkResp_t
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	if !out.NeedUpdate {
		return false, nil
	}
	uc.lock.Lock()
	uc.pending = &releaseInfo_t{Version: out.LatestVersion, DownloadURL: out.DownloadURL}
	uc.lock.Unlock()
	return true, nil
}

// 查到的待安装版本(nil 表示当前已是最新). 演示/测试断言用.
func (uc *updateClient_t) Pending() *releaseInfo_t {
	uc.lock.Lock()
	defer uc.lock.Unlock()
	return uc.pending
}

func (uc *updateClient_t) Close() {
	uc.stopOnce.Do(func() { close(uc.stopCh) })
	uc.lock.Lock()
	fn := uc.leaveFn
	uc.lock.Unlock()
	if fn != nil {
		fn()
	}
	uc.client.CloseForTest()
}
