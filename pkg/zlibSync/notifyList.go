package zlibSync

import (
	"unsafe"
)

/*
等待通知对象。
已确认runtime 内部已经有有关是否有新的等待者的优化。调用者再加一遍不会变快。可能会更慢。
*/
// Approximation of notifyList in runtime/sema.go. Size and alignment must
// agree.
type NotifyList struct {
	wait   uint32
	notify uint32
	lock   uintptr
	head   unsafe.Pointer
	tail   unsafe.Pointer
}

func (l *NotifyList) AddWaitTicket() uint32 {
	return runtime_notifyListAdd(l)
}

func (l *NotifyList) Wait(t uint32) {
	runtime_notifyListWait(l, t)
}

func (l *NotifyList) NotifyAll() {
	runtime_notifyListNotifyAll(l)
}

func (l *NotifyList) NotifyOne() {
	runtime_notifyListNotifyOne(l)
}

// 注意golang1.11 需要在当前目录加一个 empty.s 文件才可以编译。
// See runtime/sema.go for documentation.
//
//go:linkname runtime_notifyListAdd sync.runtime_notifyListAdd
func runtime_notifyListAdd(l *NotifyList) uint32

// See runtime/sema.go for documentation.
//
//go:linkname runtime_notifyListWait sync.runtime_notifyListWait
func runtime_notifyListWait(l *NotifyList, t uint32)

// See runtime/sema.go for documentation.
//
//go:linkname runtime_notifyListNotifyAll sync.runtime_notifyListNotifyAll
func runtime_notifyListNotifyAll(l *NotifyList)

// See runtime/sema.go for documentation.
//
//go:linkname runtime_notifyListNotifyOne sync.runtime_notifyListNotifyOne
func runtime_notifyListNotifyOne(l *NotifyList)

