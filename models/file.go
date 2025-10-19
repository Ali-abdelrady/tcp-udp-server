package models

import (
	"os"
	"time"
)

type FileChunk struct {
	FileID   uint32
	Seq      uint32
	FileSize uint32
	// FileName string
	Data     []byte
	ClientID uint16
}

type ReceiveSession struct {
	File       *os.File
	FileID     uint32
	FileName   string
	Expected   uint32
	Received   uint32
	TotalChunk uint32
	Chunks     map[uint32]bool
	ChunkChan  chan FileChunk
	ClientID   uint16
}

type SendSession struct {
	FileID      uint32
	File        *os.File
	FileSize    uint32
	FileName    string
	TotalChunks uint32
	CreatedAt   time.Time // ⬅ added
}
