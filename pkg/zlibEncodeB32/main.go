package zlibEncodeB32

import (
	"encoding/base32"
	"sync"
)

func HgmEncodeSlice(dst []byte, src []byte) {
	if len(src) == 16 {
		Gen_hgmEncodeSrc16(dst, src)
		return
	}
	hgmInit()
	gHgmEncode.Encode(dst, src)
}

func hgmInit() {
	gHgmEncodeOnce.Do(func() {
		gHgmEncode = base32.NewEncoding(HgmEncodingS).WithPadding(base32.NoPadding)
	})
}

const HgmEncodingS = "123456789abcdefghjkmnpqrstuvwxyz"

const encodeRand = HgmEncodingS

var gHgmEncode *base32.Encoding

var gHgmEncodeOnce sync.Once

