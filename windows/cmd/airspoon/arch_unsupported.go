//go:build windows && !amd64 && !arm64

package main

// AirSpoon Sender рассчитан на 64-битную Windows: адреса и структуры COM/DXGI/
// Media Foundation в пакетах dxgi и mf описаны под 64 бита. На 32-битной сборке
// (GOARCH=386, arm) программа не собирается, вместо того чтобы собраться и падать
// при запуске. Сообщение компилятора ниже и есть объяснение.
var _ = ОШИБКА_СБОРКИ_нужна_64_битная_Windows_GOARCH_amd64_или_arm64
