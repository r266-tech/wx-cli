//go:build !darwin

package keystore

func Save(Record) error                    { return ErrUnavailable }
func Load(string, string) (*Record, error) { return nil, ErrUnavailable }
func Delete(string, string) error          { return ErrUnavailable }
