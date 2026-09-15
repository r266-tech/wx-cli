//go:build !darwin

package keystore

func Available() bool                                       { return false }
func LoadWithAuthorization(string, string) (*Record, error) { return nil, ErrUnavailable }

func Save(Record) error                    { return ErrUnavailable }
func Load(string, string) (*Record, error) { return nil, ErrUnavailable }
func Delete(string, string) error          { return ErrUnavailable }
