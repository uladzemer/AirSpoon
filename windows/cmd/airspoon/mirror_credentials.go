package main

import (
	"bytes"
	"fmt"
	"log"

	"airspoon/internal/airplay"
)

// saveMirrorPairing only requires durable storage for a first receiver pin or
// new PIN credentials. Transient controller keys are regenerated on each use.
func saveMirrorPairing(store interface {
	Lookup(string) *airplay.SavedCredentials
	SaveClientPairing(string, *airplay.AirPlayClient) error
}, deviceID string, client *airplay.AirPlayClient, newPINKeys bool) error {
	keys := client.PairKeys
	if keys == nil {
		return nil
	}
	saved := store.Lookup(deviceID)
	if saved == nil {
		saved = &airplay.SavedCredentials{}
	}
	newPin := len(keys.ReceiverPublic) > 0 && len(saved.ReceiverPublic) == 0
	identityChanged := keys.ReceiverID != saved.ReceiverID || !bytes.Equal(keys.ReceiverPublic, saved.ReceiverPublic)
	controllerChanged := !keys.Transient && (client.PairingID != saved.PairingID ||
		!bytes.Equal(keys.Ed25519Public, saved.Ed25519Public) ||
		!bytes.Equal(keys.Ed25519Private.Seed(), saved.Ed25519Seed) || saved.Transient)
	protocolChanged := !keys.Transient && client.PairingProtocol() != saved.PairingProtocol
	if !identityChanged && !controllerChanged && !protocolChanged {
		return nil
	}
	if err := store.SaveClientPairing(deviceID, client); err != nil {
		if newPin || newPINKeys && controllerChanged {
			return fmt.Errorf("сохранение ключей сопряжения: %w", err)
		}
		log.Printf("предупреждение: ключи сопряжения не сохранены: %v", err)
		return nil
	}
	log.Println("ключи сопряжения сохранены")
	return nil
}
