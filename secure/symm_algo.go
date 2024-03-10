package secure

type ProvideSecretKey func(pubKey string) ([]byte, error)

type SymmAlgo interface {
	Encrypt(data []byte, pubKey string) ([]byte, error)
	Decrypt(data []byte, pubKey string) ([]byte, error)
}
