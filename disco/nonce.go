package disco

import (
	"crypto/rand"
	"fmt"
	"strconv"
)

func NewNonce() string {
	buf := make([]byte, 1)
	n, _ := rand.Read(buf)
	if n != 1 {
		return "0"
	}
	if buf[0] == 0 {
		return NewNonce()
	}
	return fmt.Sprintf("%d", buf[0])
}

func MustParseNonce(nonce string) byte {
	ret, err := strconv.ParseUint(nonce, 10, 8)
	if err != nil {
		return 0
	}
	return byte(ret)
}
