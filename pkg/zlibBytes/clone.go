package zlibBytes

func Clone(buf []byte) []byte {
	return append([]byte(nil), buf...)
}

