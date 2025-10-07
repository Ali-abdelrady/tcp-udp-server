package utils

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

func GenerateTimestampID() uint32 {
	// Use milliseconds for longer wrap-around (≈49 days)
	timestamp := uint32(time.Now().UnixNano() / 1e6)

	var randBytes [2]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		panic("failed to generate random bytes: " + err.Error())
	}
	randomPart := binary.BigEndian.Uint16(randBytes[:])

	// Combine: upper 16 bits = timestamp, lower 16 bits = random
	return (timestamp << 16) | uint32(randomPart)
}
