package zlibStrings

import (
	"reflect"
	"unsafe"
)

// 输出的生命周期要比输入的生命周期短或相同.否则会内存异常
func ByteArrayToStringNoAlloc(b []byte) string {
	var s string
	sx := (*reflect.StringHeader)(unsafe.Pointer(&s))
	bx := (*reflect.SliceHeader)(unsafe.Pointer(&b))
	sx.Data = bx.Data
	sx.Len = bx.Len
	return s
}

