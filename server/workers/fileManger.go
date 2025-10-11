package workers

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	CHUNKSIZE = 65000
)

type FileChunk struct {
	FileID   uint32
	Seq      uint32
	FileSize uint32
	Data     []byte
}

type FileSession struct {
	File      *os.File
	Expected  uint32
	Received  uint32
	Chunks    map[uint32]bool
	ChunkChan chan FileChunk
}

type FileManger struct {
	files map[uint32]*FileSession
	Ops   chan FileChunk
}

func NewFileManger() *FileManger {
	fm := &FileManger{
		files: make(map[uint32]*FileSession),
		Ops:   make(chan FileChunk),
	}

	go fm.run()

	return fm
}

func (fm *FileManger) run() {
	for chunk := range fm.Ops {
		session, exists := fm.files[chunk.FileID]

		// Create new session for new fileId
		if !exists && chunk.Seq == 0 {
			fmt.Printf("Recieved First Seq = %v , fileId = %v , fileSize = %v\n", chunk.Seq, chunk.FileID, chunk.FileSize)
			// Save file in project root
			wd, err := os.Getwd()
			if err != nil {
				fmt.Println("Error getting working directory:", err)
				return
			}

			filePath := filepath.Join(wd, fmt.Sprintf("file_%d.csv", chunk.FileID))
			file, err := os.Create(filePath)
			if err != nil {
				fmt.Println("Error creating file:", err)
				return
			}

			session = &FileSession{
				File:      file,
				Expected:  chunk.FileSize,
				Received:  0,
				Chunks:    make(map[uint32]bool),
				ChunkChan: make(chan FileChunk, 50),
			}

			fm.files[chunk.FileID] = session
			go fm.handleFile(session)

		}

		if session == nil {
			continue
		}

		session.ChunkChan <- chunk
	}
}

func (fm *FileManger) handleFile(session *FileSession) {
	for chunk := range session.ChunkChan {
		// Check if the chunk duplicated
		if session.Chunks[chunk.Seq] {
			fmt.Printf("⚠ Duplicate Seq = %d ignored \n", chunk.Seq)
			continue
		}

		session.Chunks[chunk.Seq] = true

		offset := int64(chunk.Seq) * int64(CHUNKSIZE)
		session.File.WriteAt(chunk.Data, offset)
		session.Received += uint32(len(chunk.Data))
		fmt.Printf("Recieved (%d/%d) seq = %d \n", session.Received, session.Expected, chunk.Seq)

		if session.Received >= session.Expected {
			fmt.Printf("✅ File %d done (%.2f KB)\n", chunk.FileID, float64(session.Expected)/1024)
			session.File.Close()
			close(session.ChunkChan)
			delete(fm.files, chunk.FileID)
			return
		}
	}
}

