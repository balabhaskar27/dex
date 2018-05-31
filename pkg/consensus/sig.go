package consensus

import "github.com/dfinity/go-dfinity-crypto/bls"

// PK is a serialized public key.
type PK []byte

func (p PK) Get() (bls.PublicKey, error) {
	var pk bls.PublicKey
	err := pk.Deserialize(p)
	if err != nil {
		return bls.PublicKey{}, err
	}

	return pk, nil
}

func (p PK) Addr() Addr {
	return SHA3(p).Addr()
}

// SK is a serialized secret key
type SK []byte

func (s SK) Get() (bls.SecretKey, error) {
	var sk bls.SecretKey
	err := sk.SetLittleEndian(s)
	if err != nil {
		return bls.SecretKey{}, err
	}

	return sk, nil
}