package zlibBytes

import (
	"encoding/binary"
)

/*
结果字节长度 最大可以encode的数.
1 127
2 16383
3 2097151
4 268435455
5 34359738367
6 4398046511103
7 562949953421311
8 72057594037927935
9 9223372036854775807
10 18446744073709551615
*/
func (w *BufWriter) WriteUvarint(v uint64) {
	*w = binary.AppendUvarint(*w, v)
}

