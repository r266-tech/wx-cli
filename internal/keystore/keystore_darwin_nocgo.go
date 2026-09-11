//go:build darwin && !cgo

package keystore

import "errors"

func Save(Record) error { return errors.New("Keychain unavailable in cgo-disabled build") }
func Load(string, string) (*Record, error) {
	return nil, errors.New("Keychain unavailable in cgo-disabled build")
}
func Delete(string, string) error { return errors.New("Keychain unavailable in cgo-disabled build") }
