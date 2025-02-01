package shortener

import (
	"errors"
	"math"
	"strings"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var ErrBadBase62 = errors.New("shortener: not a base62 string")

func Encode(n uint64) string {
	if n == 0 {
		return string(alphabet[0])
	}
	var buf [11]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = alphabet[n%62]
		n /= 62
	}
	return string(buf[i:])
}

func Decode(s string) (uint64, error) {
	if s == "" || len(s) > 11 {
		return 0, ErrBadBase62
	}
	var n uint64
	for _, r := range s {
		index := strings.IndexRune(alphabet, r)
		if index < 0 {
			return 0, ErrBadBase62
		}
		if n > (math.MaxUint64-uint64(index))/62 {
			return 0, ErrBadBase62
		}
		n = n*62 + uint64(index)
	}
	return n, nil
}
