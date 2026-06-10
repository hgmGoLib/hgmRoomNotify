module github.com/hgmGoLib/hgmRoomNotifyExample

go 1.24.1

// 例子始终对着同仓库当前 commit 的 hgmRoomNotify 源码编译, 不依赖已发布版本.
replace github.com/hgmGoLib/hgmRoomNotify => ../

require (
	github.com/chromedp/chromedp v0.14.2
	github.com/hgmGoLib/hgmRoomNotify v0.0.0-00010101000000-000000000000
)

require (
	github.com/chromedp/cdproto v0.0.0-20250724212937-08a3db8b4327 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20250725192818-e39067aee2d2 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	golang.org/x/sys v0.34.0 // indirect
)
