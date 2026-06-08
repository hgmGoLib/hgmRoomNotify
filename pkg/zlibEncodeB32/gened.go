package zlibEncodeB32

// 要求输入 src 的长度是 16,dst 长度至少 26
func Gen_hgmEncodeSrc16(dst []byte, src []byte) {
	_ = src[15]
	_ = dst[25]

	dst[0] = encodeRand[src[0]>>3]
	dst[1] = encodeRand[((src[1]>>6)&0x1F)|((src[0]<<2)&0x1F)]
	dst[2] = encodeRand[(src[1]>>1)&0x1F]
	dst[3] = encodeRand[((src[2]>>4)&0x1F)|((src[1]<<4)&0x1F)]
	dst[4] = encodeRand[(src[3]>>7)|((src[2]<<1)&0x1F)]
	dst[5] = encodeRand[(src[3]>>2)&0x1F]
	dst[6] = encodeRand[(src[4]>>5)|((src[3]<<3)&0x1F)]
	dst[7] = encodeRand[src[4]&0x1F]
	dst[8] = encodeRand[src[5]>>3]
	dst[9] = encodeRand[((src[6]>>6)&0x1F)|((src[5]<<2)&0x1F)]
	dst[10] = encodeRand[(src[6]>>1)&0x1F]
	dst[11] = encodeRand[((src[7]>>4)&0x1F)|((src[6]<<4)&0x1F)]
	dst[12] = encodeRand[(src[8]>>7)|((src[7]<<1)&0x1F)]
	dst[13] = encodeRand[(src[8]>>2)&0x1F]
	dst[14] = encodeRand[(src[9]>>5)|((src[8]<<3)&0x1F)]
	dst[15] = encodeRand[src[9]&0x1F]
	dst[16] = encodeRand[src[10]>>3]
	dst[17] = encodeRand[((src[11]>>6)&0x1F)|((src[10]<<2)&0x1F)]
	dst[18] = encodeRand[(src[11]>>1)&0x1F]
	dst[19] = encodeRand[((src[12]>>4)&0x1F)|((src[11]<<4)&0x1F)]
	dst[20] = encodeRand[(src[13]>>7)|((src[12]<<1)&0x1F)]
	dst[21] = encodeRand[(src[13]>>2)&0x1F]
	dst[22] = encodeRand[(src[14]>>5)|((src[13]<<3)&0x1F)]
	dst[23] = encodeRand[src[14]&0x1F]
	dst[24] = encodeRand[src[15]>>3]
	dst[25] = encodeRand[((src[15] << 2) & 0x1F)]
}

