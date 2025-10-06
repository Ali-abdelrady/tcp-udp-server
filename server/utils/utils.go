package utils

import (
	"crypto/rand"
	"time"
)

func GenerateTimestampID() uint32 {
	timestamp := uint32(time.Now().UnixNano())

	var randByte [1]byte
	_, err := rand.Read(randByte[:])
	if err != nil {
		panic("failed to generate random byte: " + err.Error())
	}
	randomSuffix := uint32(randByte[0])

	// Combine: upper 24 bits = timestamp, lower 8 bits = random
	return (timestamp << 8) | randomSuffix
}
