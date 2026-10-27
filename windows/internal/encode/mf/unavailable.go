package mf

import "errors"

// ErrUnavailable — Media Foundation или программный кодер H.264 в этой Windows
// не установлены (типичный случай: редакция N/KN без «Пакета мультимедийных
// функций»). errors.Is(err, ErrUnavailable) позволяет показать пользователю
// понятный совет вместо общего «не получилось захватить экран».
var ErrUnavailable = errors.New("Media Foundation недоступна")

// unavailableError помечает исходную ошибку признаком ErrUnavailable.
type unavailableError struct{ err error }

func (e *unavailableError) Error() string        { return e.err.Error() }
func (e *unavailableError) Unwrap() error        { return e.err }
func (e *unavailableError) Is(target error) bool { return target == ErrUnavailable }

// markUnavailable оборачивает err признаком ErrUnavailable (nil остаётся nil).
func markUnavailable(err error) error {
	if err == nil {
		return nil
	}
	return &unavailableError{err}
}

// hrClassNotRegistered — REGDB_E_CLASSNOTREG: класс COM не зарегистрирован (на
// N-редакциях Windows так выглядит отсутствие программного кодера H.264).
const hrClassNotRegistered uint32 = 0x80040154
