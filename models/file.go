package models

import (
	"os"
	"time"
)

type FileChunk struct {
	FileID   uint32
	Seq      uint16
	Data     []byte
	ClientID uint16
}

type FileMeta struct {
	FileID    uint32
	FileSize  uint32
	FileName  []byte
	ChunkSize uint16
	ClientID  uint16
}

type ReceiveSession struct {
	File       *os.File
	FileID     uint32
	FileName   string
	Expected   uint16
	Received   uint16
	TotalChunk uint16
	Chunks     map[uint16]bool
	ChunkChan  chan FileChunk
	ClientID   uint16
	ChunkSize  uint16
}

type SendSession struct {
	FileID      uint32
	File        *os.File
	FileSize    uint32
	FileName    string
	TotalChunks uint16
	CreatedAt   time.Time // ⬅ added
}
