//go:build windows

// Пакет com — минимальные помощники для вызова COM-методов Windows через
// таблицы виртуальных методов (vtable) без cgo и без внешних библиотек.
// Интерфейсы описываются слотами: слот 0 — QueryInterface, 1 — AddRef,
// 2 — Release, дальше методы интерфейса в порядке объявления в заголовке.
package com

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GUID — идентификатор интерфейса, класса или атрибута.
type GUID = windows.GUID

// ParseGUID разбирает "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx". При ошибке паникует:
// функция нужна для константных идентификаторов, заданных в коде.
func ParseGUID(s string) GUID {
	g, err := windows.GUIDFromString("{" + s + "}")
	if err != nil {
		panic(fmt.Sprintf("com: неверный GUID %q: %v", s, err))
	}
	return g
}

// Object — COM-объект: указатель на структуру, первое поле которой — указатель
// на таблицу методов. Значение *Object — это и есть указатель на интерфейс.
type Object struct {
	vtbl *[1024]uintptr
}

// Call вызывает метод по номеру слота таблицы. Первым аргументом COM-вызова
// автоматически передаётся this. Возвращает сырое значение (HRESULT для
// большинства методов).
//
// uintptrescapes: указатели, преобразованные в uintptr прямо в списке аргументов
// вызова, остаются живыми на время вызова и не лежат в перемещаемом стеке.
//
//go:uintptrescapes
func (o *Object) Call(slot int, args ...uintptr) uintptr {
	var buf [20]uintptr
	if len(args)+1 > len(buf) {
		panic("com: слишком много аргументов вызова")
	}
	buf[0] = uintptr(unsafe.Pointer(o))
	copy(buf[1:], args)
	r, _, _ := syscall.SyscallN(o.vtbl[slot], buf[:len(args)+1]...)
	return r
}

// Slot возвращает адрес метода (для отладки и тестов).
func (o *Object) Slot(slot int) uintptr { return o.vtbl[slot] }

// Release уменьшает счётчик ссылок; nil допустим.
func (o *Object) Release() {
	if o != nil {
		o.Call(2)
	}
}

// AddRef увеличивает счётчик ссылок.
func (o *Object) AddRef() {
	if o != nil {
		o.Call(1)
	}
}

// QueryInterface запрашивает другой интерфейс объекта.
func (o *Object) QueryInterface(iid *GUID) (*Object, error) {
	var out *Object
	hr := o.Call(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if err := Check("QueryInterface", hr); err != nil {
		return nil, err
	}
	return out, nil
}

// Error — ошибка с кодом HRESULT.
type Error struct {
	Op string
	HR uint32
}

func (e *Error) Error() string { return fmt.Sprintf("%s: HRESULT 0x%08X", e.Op, e.HR) }

// Check превращает отрицательный HRESULT в ошибку; успех (в том числе S_FALSE) — nil.
func Check(op string, hr uintptr) error {
	if int32(hr) < 0 {
		return &Error{Op: op, HR: uint32(hr)}
	}
	return nil
}

// HR возвращает код HRESULT из ошибки (если она получена через Check).
func HR(err error) (uint32, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.HR, true
	}
	return 0, false
}

// Is сообщает, что err — ошибка с указанным HRESULT.
func Is(err error, hr uint32) bool {
	code, ok := HR(err)
	return ok && code == hr
}

// Init инициализирует COM (многопоточная квартира) в текущем потоке ОС и
// закрепляет горутину за потоком. Возвращаемая функция отменяет инициализацию
// и открепляет поток; вызывать её нужно в той же горутине.
func Init() (func(), error) {
	runtime.LockOSThread()
	// S_FALSE (поток уже инициализирован) приходит как Errno(1) и всё равно
	// требует парного CoUninitialize.
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && err != syscall.Errno(1) {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("инициализация COM: %w", err)
	}
	return func() {
		windows.CoUninitialize()
		runtime.UnlockOSThread()
	}, nil
}
