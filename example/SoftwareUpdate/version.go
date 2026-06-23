package main

import (
	"strconv"
	"strings"
)

// 比较两个点分版本号(如 "1.2.0" vs "1.10.3"). 返回 (sign, ok):
// sign: a>b 为 1, a==b 为 0, a<b 为 -1. ok=false 表示无法比较(格式不认识/为空)。
// 关键: ok=false 时调用方必须回源(去问 check api), 绝不能因为"比不出来"就当成不用更新而跳过。
func compareVersion(a string, b string) (int, bool) {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka || !okb {
		return 0, false
	}
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y uint64
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x > y {
			return 1, true
		}
		if x < y {
			return -1, true
		}
	}
	return 0, true
}

// 把 "1.2.0" 解析成 [1,2,0]. 空串或任一段不是数字都返回 ok=false.
func parseVersion(s string) ([]uint64, bool) {
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	out := make([]uint64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}
