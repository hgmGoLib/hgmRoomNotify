package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/hgmGoLib/hgmRoomNotify"
)

// 软件更新通道的房间 id. 同一发布通道(这里是 stable)的所有客户端都进这个房间, 监听"有没有新版本".
const updateRoomId = "appUpdate:stable"

// 一个可下载的发布版本.
type releaseInfo_t struct {
	Version     string // 版本号, 形如 "1.2.0". 同时被放进房间 CVersionId 当加速字段.
	DownloadURL string // 该版本的下载地址.
}

// check api 的返回. 客户端带着自己当前版本来问, 服务端告知是否要更新 + 下一个版本下载地址.
// 这是更新对接里的【可靠数据源】: "要不要更新 / 去哪下" 一律以它为准, CVersionId 只是加速通知。
type checkResp_t struct {
	NeedUpdate    bool
	LatestVersion string
	DownloadURL   string
}

// 更新服务端: 持有"当前最新发布版本" + 一个 hgmRoomNotify ws 层.
type updateServer_t struct {
	latest   atomic.Pointer[releaseInfo_t]
	wsServer hgmRoomNotify.ServerManager
}

func newUpdateServer(initial releaseInfo_t) *updateServer_t {
	s := &updateServer_t{}
	s.latest.Store(&initial)
	return s
}

// 两条路由: /ws 是 hgmRoomNotify 通知通道(实时戳"有新版本了"); /update/check 是可靠的版本查询接口.
func (s *updateServer_t) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		s.wsServer.ServeHTTP(w, r)
	})
	mux.HandleFunc("/update/check", s.handleCheck)
	return mux
}

// 可靠数据源: 客户端带自己当前版本来问, 服务端比较版本后告知是否要更新 + 下一个版本下载地址.
// 注意这里完全不依赖 CVersionId/ws —— 即使 ws 从没通知到, 客户端启动时问这一下也能得到正确答案.
func (s *updateServer_t) handleCheck(w http.ResponseWriter, r *http.Request) {
	clientVer := r.URL.Query().Get("version")
	latest := s.latest.Load()
	resp := checkResp_t{LatestVersion: latest.Version}
	if sign, ok := compareVersion(latest.Version, clientVer); ok && sign > 0 {
		resp.NeedUpdate = true
		resp.DownloadURL = latest.DownloadURL
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// 发布一个新版本: 更新"最新版本", 再 FireChange 把版本号放进 CVersionId 实时戳给在线客户端(加速).
// 客户端收到后用 CVersionId 跟自己当前版本比, 确实更新了才去 check api 拿权威结果和下载地址.
func (s *updateServer_t) Publish(version string, downloadURL string) {
	s.latest.Store(&releaseInfo_t{Version: version, DownloadURL: downloadURL})
	s.wsServer.FireChange(hgmRoomNotify.RoomEvent_t{
		RoomId:     updateRoomId,
		CVersionId: version, // 加速字段: 当前最新版本号. 不保证送达, 客户端最终以 check api 为准.
	})
}

// 发一次"裸戳"(不带 CVersionId, 也不改最新版本). 用于演示 CVersionId 缺失时客户端必须回源 check、不能瞎跳过.
// (FireChange 不带 CVersionId 会把房间里存的 CVersionId 清空, 正是它"不保证总是有"的一种情形。)
func (s *updateServer_t) NotifyBare() {
	s.wsServer.FireChange(hgmRoomNotify.RoomEvent_t{RoomId: updateRoomId})
}
