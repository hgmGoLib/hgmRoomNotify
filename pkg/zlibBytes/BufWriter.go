package zlibBytes

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/hgmGoLib/hgmRoomNotify/pkg/zlibStrings"
	"math"
	"unicode/utf8"
)

type BufWriter []byte

func (w *BufWriter) Reset() {
	*w = (*w)[:0]
}

func (w *BufWriter) GetBytes() []byte {
	return *w
}

func (w *BufWriter) Clone() []byte {
	return Clone(*w)
}

func (w *BufWriter) ToSlicePtr() *[]byte {
	return (*[]byte)(w)
}

func (w *BufWriter) GetLen() int {
	return len(*w)
}

func (w *BufWriter) GetString() string {
	return string(*w)
}

// 保证从 pos 处，向后面有 toWrite 个字节的buffer。不会移动pos的位置
// 可能会丢失 pos 后面已经写入的信息。(适合从当前位置，一直写入的api。）
func (w *BufWriter) TryGrow(toWrite int) {
	needSize := len(*w) + toWrite
	thisCap := cap(*w)
	if needSize > thisCap {
		targetSize := thisCap*2 + toWrite
		if targetSize < 32 {
			targetSize = 32
		}
		oldPos := len(*w)
		newBuf := make([]byte, oldPos, targetSize)
		copy(newBuf, *w)
		*w = newBuf
	}
}

func (w *BufWriter) GetAllocNoUseBuf() []byte {
	return (*w)[len(*w):cap(*w)]
}

// 保证从 pos 处，向后面有 toWrite 个字节的buffer。不会移动pos的位置
// 不会丢失任何已经写入的信息。(适合跳来跳去的api）
func (w *BufWriter) tryGrowWithSetPos(toWrite int) {
	needSize := len(*w) + toWrite
	thisCap := cap(*w)
	if needSize > thisCap {
		targetSize := thisCap*2 + toWrite
		if targetSize < 32 {
			targetSize = 32
		}
		oldPos := len(*w)
		newBuf := make([]byte, targetSize, targetSize)
		copy(newBuf, (*w)[:cap(*w)])
		*w = newBuf[:oldPos]
	}
}

func (w *BufWriter) WriteByte(b byte) error {
	*w = append(*w, b)
	return nil
}

func (w *BufWriter) WriteByte_(b byte) {
	*w = append(*w, b)
}

func (w *BufWriter) Write_(buf []byte) {
	*w = append(*w, buf...)
}

func (w *BufWriter) WriteString_(s string) {
	*w = append(*w, s...)
}

func (w *BufWriter) WriteString(s string) error {
	*w = append(*w, s...)
	return nil
}

func (w *BufWriter) WriteLittleEndUint64(v uint64) {
	w.TryGrow(8)
	binary.LittleEndian.PutUint64((*w)[len(*w):len(*w)+8], v)
	(*w) = (*w)[:len(*w)+8]
}

// 向后多取出xx长度的数组来，调用者可以直接修改里面的值。
// 修改了取出的数组后，如果想让 BufWriter 在 GetBytes 时能得到修改的值，需要调用一下 AddPos
// 本调用可能增加内部buf的分配大小，但是不会修改有效数据的长度。
// 不保证给出的数组被初始化了。不会丢失
func (w *BufWriter) GetHeadBuffer(size int) []byte {
	w.tryGrowWithSetPos(size)
	return (*w)[len(*w) : len(*w)+size]
}

func (w *BufWriter) AddPos(offset int) {
	w.tryGrowWithSetPos(offset)
	(*w) = (*w)[:len((*w))+offset]
}

// 先 GetHeadBuffer 然后 AddPos
func (w *BufWriter) GetBufferAndAddPos(size int) []byte {
	w.tryGrowWithSetPos(size)
	buf := (*w)[len(*w) : len(*w)+size]
	(*w) = (*w)[:len((*w))+size]
	return buf
}

// 设置当前buf 的位置.
func (w *BufWriter) SetPos(pos int) {
	if pos < 0 {
		panic(`BufWriter.SetPos pos<0`)
	}
	offset := pos - len(*w)
	w.AddPos(offset)
}

func (w *BufWriter) WriteLittleEndUint16(v uint16) {
	buf := w.GetBufferAndAddPos(2)
	binary.LittleEndian.PutUint16(buf, v)
}

func (w *BufWriter) WriteLittleEndUint32(v uint32) {
	buf := w.GetBufferAndAddPos(4)
	binary.LittleEndian.PutUint32(buf, v)
}

func (w *BufWriter) WriteLittleEndInt32(v int32) {
	buf := w.GetBufferAndAddPos(4)
	binary.LittleEndian.PutUint32(buf, uint32(v))
}

func (w *BufWriter) WriteFloat32(v float32) {
	w.WriteLittleEndUint32(math.Float32bits(v))
}

func (w *BufWriter) WriteFloat64(v float64) {
	w.WriteLittleEndUint64(math.Float64bits(v))
}

func (w *BufWriter) WriteRune_(r rune) {
	*w = utf8.AppendRune(*w, r)
}

func (w *BufWriter) Println(objList ...any) {
	*w = fmt.Appendln(*w, objList...)
}

func (w *BufWriter) TrimSuffix(s string) {
	*w = bytes.TrimSuffix(*w, []byte(s))
}

func (w *BufWriter) WriteRune(r rune) (n int, err error) {

	if uint32(r) < utf8.RuneSelf {
		w.WriteByte_(byte(r))
		return 1, nil
	}
	oldLen := len(*w)
	*w = utf8.AppendRune(*w, r)
	return len(*w) - oldLen, nil
}

// 给个回调,在回调里面可以向这个对象后面写东西,在返回时退回最开始的位置,并且用字符串返回刚才写的东西的内容
// 注意只能开一层,不能递归.
func (w *BufWriter) HeadCbToString(fn func()) string {
	pos := w.GetLen()
	fn()
	out := string(w.GetBytes()[pos:])
	w.SetPos(pos)
	return out
}

func (w *BufWriter) GetStringUnsafe() string {
	return zlibStrings.ByteArrayToStringNoAlloc(*w)
}

