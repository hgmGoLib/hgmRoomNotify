package zlibBytes

import (
	"io"
)

/*
满足各种“官方”的api接口。
*/
// ReadFrom reads data from r until EOF and appends it to the buffer, growing the buffer as needed. The return value n is the number of bytes read. Any error except io.EOF encountered during the read is also returned.
// 从 r 里面，读到 EOF。内部的buffer 会随着 r 的读入而愈来愈大。
func (w *BufWriter) ReadFrom(r io.Reader) (n int64, err error) {
	for {

		w.TryGrow(512)
		m, e := r.Read((*w)[len(*w):cap(*w)])

		*w = (*w)[:len(*w)+m]
		n += int64(m)
		if e == io.EOF {
			break
		}
		if e != nil {
			return n, e
		}
	}
	return n, nil
}

func (w *BufWriter) Write(buf []byte) (n int, err error) {
	*w = append(*w, buf...)
	return len(buf), nil
}

