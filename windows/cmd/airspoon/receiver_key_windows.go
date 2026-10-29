package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"airspoon/internal/airplay"
	"airspoon/internal/trayctl"
	"airspoon/internal/traywin"
)

// Carry the /info device ID back to the tray rather than using the display
// name or the differently cased mDNS ID to select credentials for removal.
type receiverKeyMismatchError struct {
	name, deviceID string
	err            error
}

func (e *receiverKeyMismatchError) Error() string { return e.err.Error() }
func (e *receiverKeyMismatchError) Unwrap() error { return e.err }

func confirmReceiverKeyChange(title, text string) bool {
	return traywin.Confirm(title, text)
}

func (s *mirrorStreamer) runWithReceiverKeyConfirmation(ctx context.Context, o mirrorOptions) error {
	err := s.run(ctx, o)
	var mismatch *receiverKeyMismatchError
	if !errors.As(err, &mismatch) || s.confirmReceiverKeyChange == nil || ctx.Err() != nil {
		return err
	}
	name := mismatch.name
	if name == "" {
		name = mismatch.deviceID
	}
	// Receiver names are network input; MessageBox rejects embedded NULs.
	name = strings.ReplaceAll(name, "\x00", "�")
	text := fmt.Sprintf("Телевизор «%s» представился другим ключом — например, приложение на нём переустановили.\n\nЕсли вы этого не делали, кто-то в сети может выдавать себя за телевизор.\n\nПодключиться и запомнить новый ключ?", name)
	if !s.confirmReceiverKeyChange(trayctl.AppName, text) {
		// Reuse the controller's explicit-cancellation path to stop retries.
		return trayctl.ErrPINCanceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := airplay.NewCredentialStore(o.credFile)
	if err != nil {
		return fmt.Errorf("хранилище ключей сопряжения: %w", err)
	}
	if err := store.ForgetReceiverIdentity(mismatch.deviceID); err != nil {
		return fmt.Errorf("сброс ключа телевизора: %w", err)
	}
	o.forcePair = true
	return s.run(ctx, o)
}
