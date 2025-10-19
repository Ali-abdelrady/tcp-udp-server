package server

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hole-punching-v2/models"
	"hole-punching-v2/server/utils"
	"hole-punching-v2/server/workers"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Udp struct {
	Port           string
	parserChan     chan models.RawPacket
	writeChan      chan models.Packet
	generateChan   chan models.Packet
	trackingChan   chan models.Packet
	fileChunksChan chan models.FileChunk

	receivedFiles sync.Map // map[uint32]*models.ReceiveSession
	sendOutFiles  sync.Map // map[uint32]*models.SendSession

	// fileManger   *workers.FileManager
	clientManger workers.ClientManager
	ackManger    *workers.AckManager
}

const (
	BUFFER_SIZE = 65507
	CHUNKSIZE   = 1424
)

// OPCODES
const (
	OpRegister     byte = iota // 0
	OpPing                     // 1
	OpMessage                  // 2
	OpPong                     // 3
	OpFileChunk                // 4
	OpAck                      //5
	OpFileMeta                 //6
	OpChunkRequest             //7
)

func (s *Udp) StartServer() {
	// Resolve a udp addr
	udpAddr, err := net.ResolveUDPAddr("udp4", s.Port)
	if err != nil {
		fmt.Println("falied to resolve udp address,err: ", err)
		os.Exit(1)
	}

	// Start listening
	connection, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		fmt.Println("falied to craete listener,err: ", err)
		os.Exit(1)
	}
	defer connection.Close()
	fmt.Printf("✅ server listening on addr %s \n", s.Port)

	// Initialze Buffer Pool

	// Initialize Channel and workers
	s.writeChan = make(chan models.Packet, 50)
	s.parserChan = make(chan models.RawPacket, 50)
	s.generateChan = make(chan models.Packet, 50)
	s.trackingChan = make(chan models.Packet, 200)
	s.fileChunksChan = make(chan models.FileChunk, 100)

	s.clientManger = *workers.NewClientManager()
	s.ackManger = workers.NewAckManager()
	// s.fileManger = workers.NewFileManger()

	// s.sendOutFiles = make(map[uint32]*models.SendSession)
	// s.receivedFiles = make(map[uint32]*models.ReceiveSession)

	// Run Workers
	for i := 0; i < 3; i++ {
		go s.parserWorker()
		go s.generatorWorker()
		go s.trackingWorker()
	}

	go s.writeWorker(connection)
	go s.startInteractiveCommandInput()
	go s.fileManagerWorker()
	go s.cleanupSendOutFiles(10 * time.Minute)

	// 🔹 Defer log for graceful shutdown
	defer utils.PrintApiLog("Server Shutdown")

	// 🔹 Setup signal handling (Ctrl+C, kill, etc.)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n🛑 Shutting down server gracefully...")
		// fmt.Println("AllocationCnt: ", s.allocationCnt)
		utils.PrintApiLog("Server Exit")
		os.Exit(0)
	}()

	buffer := make([]byte, BUFFER_SIZE)

	for {
		n, addr, err := connection.ReadFromUDP(buffer)
		if err != nil {
			fmt.Println("falied to read data, err: ", err)
			continue
		}
		if n < 7 {
			fmt.Println("no data to read: n = ", n)
			continue
		}

		dataCopy := make([]byte, n)
		copy(dataCopy, buffer[:n])

		s.parserChan <- models.RawPacket{Data: dataCopy, Addr: addr}
	}

}

//-----------Workers--------------

func (s *Udp) writeWorker(conn *net.UDPConn) {
	for pkt := range s.writeChan {
		_, err := conn.WriteToUDP(pkt.Payload, pkt.Addr)
		if err != nil {
			fmt.Println("failed to send packet")
		}
	}
}

func (s *Udp) parserWorker() {

	// Packet [opcode 1] [packetId 4] [clientId 2] [payload n]
	for {
		raw := <-s.parserChan

		if len(raw.Data) < 7 {
			continue
		}

		packet := models.Packet{
			OpCode:   raw.Data[0],
			ID:       binary.BigEndian.Uint32(raw.Data[1:5]),
			ClientID: binary.BigEndian.Uint16(raw.Data[5:7]),
			Payload:  raw.Data[7:],
			Addr:     raw.Addr,
			Length:   uint16(len(raw.Data)),
		}

		switch packet.OpCode {
		case OpAck:
			s.handleAck(packet)
		case OpPing:
			s.pingClient(packet)
		case OpRegister:
			s.registerClient(packet)
		case OpMessage:
			s.handleReceiveMessage(packet)
		case OpFileMeta:
			s.handleFileMeta(packet)
		case OpFileChunk:
			s.handleFileChunk(packet)
		case OpChunkRequest:
			s.handleChunkRequest(packet)
		}
	}
}

func (s *Udp) generatorWorker() {
	for {
		packet := <-s.generateChan

		var packetID uint32
		if packet.OpCode == OpAck || packet.OpCode == OpPong || packet.OpCode == OpFileChunk {
			packetID = packet.ID
		} else {
			packetID = utils.GenerateTimestampID()
		}

		switch packet.OpCode {
		case OpAck, OpPong, OpFileChunk:
			buf := make([]byte, 1+4+2+2+len(packet.Payload))
			buf[0] = packet.OpCode
			binary.BigEndian.PutUint32(buf[1:5], packetID)
			binary.BigEndian.PutUint16(buf[5:7], uint16(len(buf)))
			binary.BigEndian.PutUint16(buf[7:9], packet.ClientID)
			copy(buf[9:], packet.Payload)

			outgoing := models.Packet{
				Payload: buf,
				Addr:    packet.Addr,
				ID:      packetID,
				Done:    packet.Done,
			}
			// Forward ACK/PONG without creating new buffer
			s.writeChan <- outgoing
		default:
			// [opcode 1] [packetId 4] [Length 2] [clientId 2] [payload n]
			buf := make([]byte, 1+4+2+2+len(packet.Payload))
			buf[0] = packet.OpCode
			binary.BigEndian.PutUint32(buf[1:5], packetID)
			binary.BigEndian.PutUint16(buf[5:7], uint16(len(buf)))
			binary.BigEndian.PutUint16(buf[7:9], packet.ClientID)
			copy(buf[9:], packet.Payload)

			outgoing := models.Packet{
				Payload: buf,
				Addr:    packet.Addr,
				ID:      packetID,
				Done:    packet.Done,
			}
			// Packet [opcode 1] [packetId 4] [size 2] [clientId 2] [payload n]
			s.trackingChan <- outgoing
		}
	}
}

func (s *Udp) trackingWorker() {
	for packet := range s.trackingChan {
		err := s.sendWithAck(packet)
		if err != nil {
			fmt.Printf("[Tracking] Failed to deliver packet %d after retries\n", packet.ID)
		}
	}
}

func (s *Udp) fileManagerWorker() {
	for chunk := range s.fileChunksChan {
		val, exists := s.receivedFiles.Load(chunk.FileID)
		var session *models.ReceiveSession

		if exists {
			session = val.(*models.ReceiveSession)
			session.ChunkChan <- chunk

		}

		// Create new session for new fileId
		if !exists && chunk.MetaSent {

			wd, err := os.Getwd()
			if err != nil {
				fmt.Println("Error getting working directory:", err)
				return
			}

			filePath := filepath.Join(wd, fmt.Sprintf("client%d_%s", chunk.ClientID, string(chunk.Data)))
			file, err := os.Create(filePath)
			if err != nil {
				fmt.Println("Error creating file:", err)
				continue
			}

			session = &models.ReceiveSession{
				ClientID:   chunk.ClientID,
				File:       file,
				FileID:     chunk.FileID,
				FileName:   filePath,
				Expected:   chunk.FileSize,
				Received:   0,
				Chunks:     make(map[uint32]bool),
				ChunkChan:  make(chan models.FileChunk, 50),
				TotalChunk: (chunk.FileSize + CHUNKSIZE - 1) / CHUNKSIZE,
			}

			s.receivedFiles.Store(chunk.FileID, session)

			go s.handleFileReceiveSession(session)

			go s.OnMetaReceived(session)

		}

		if session == nil {
			continue
		}

	}
}

func (s *Udp) startInteractiveCommandInput() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("🟢 UDP Command Interface Started")
	fmt.Println("Available commands:")
	fmt.Println("  message <clientId> <message>")
	fmt.Println("  file <clientId> <filepath>")
	fmt.Println("  list")
	fmt.Println("  help")
	fmt.Println("------------------------------")

	for {
		fmt.Print("> ")

		if !scanner.Scan() {
			fmt.Println("\n❌ Input closed. Exiting interactive mode.")
			return
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, " ", 3)
		command := strings.ToLower(parts[0])

		switch command {

		// send message <clientId> <message>
		case "message":
			if len(parts) < 3 {
				fmt.Println("⚠ Usage: message <clientId> <text>")
				continue
			}

			clientID := parts[1]

			msg := parts[2]
			s.sendMessageToClient(clientID, msg)

		// send file <clientId> <path>
		case "file":
			if len(parts) < 3 {
				fmt.Println("⚠ Usage: file <clientId> <filepath>")
				continue
			}

			clientID, err := strconv.ParseUint(parts[1], 10, 16)
			if err != nil {
				fmt.Println("❌ Invalid client ID:", err)
				continue
			}

			filepath := parts[2]
			if _, err := os.Stat(filepath); err != nil {
				fmt.Println("❌ File not found:", filepath)
				continue
			}

			s.sendFileMeta(uint16(clientID), filepath)

		// list all clients
		case "list":
			clients := s.clientManger.ListClients()

			if len(clients) == 0 {
				fmt.Println("⚠ No connected clients.")
				continue
			}

			fmt.Println("==== Connected Clients ====")
			for id, client := range clients {
				status := "🟢"
				if !client.IsOnline {
					status = "🔴"
				}

				fmt.Printf("%s ID: %d | Addr: %-21v | LastSeen: %v | Online: %v\n",
					status, id, client.Addr, client.LastSeen.Format("15:04:05"), client.IsOnline)
			}
			fmt.Println("============================")

		// show help
		case "help":
			fmt.Println("Available commands:")
			fmt.Println("  message <clientId> <message>   - send a message to client")
			fmt.Println("  file <clientId> <path>         - send a file to client")
			fmt.Println("  list                           - show connected clients")
			fmt.Println("  help                           - show this help message")

		default:
			fmt.Printf("❌ Unknown command: '%s' (type 'help' for list)\n", command)
		}
	}
}

// ----------Handlers----------

func (s *Udp) handleAck(packet models.Packet) {

	// fmt.Printf("[Server] Got ACK for packet ID = %v \n", packet.ID)

	ch := s.ackManger.GetAck(packet.ID)
	if ch != nil {
		ch <- true
	}
}

func (s *Udp) sendWithAck(packet models.Packet) error {
	retries := 3

	// Register a pending acknowledgment channel
	s.ackManger.AddAck(packet.ID)

	ackCh := s.ackManger.GetAck(packet.ID)
	if ackCh == nil {
		return fmt.Errorf("failed to create ack channel for packet %d", packet.ID)
	}

	// fmt.Println("pendingAck:", s.pendingAck)
	defer s.ackManger.DeleteAck(packet.ID)

	for i := 0; i < retries; i++ {

		s.writeChan <- packet
		time.Sleep(1 * time.Millisecond) // 👈 helps throttle sending rate

		select {
		case <-ackCh:
			if packet.Done != nil {
				packet.Done <- true
			}
			fmt.Printf("ACK received for packet %d\n", packet.ID)
			return nil
		case <-time.After(5 * time.Second):
			fmt.Printf("Timeout for packet %d, retrying...\n", packet.ID)
		}
	}

	if packet.Done != nil {
		packet.Done <- false
	}

	return fmt.Errorf("failed to deliver packet %d", packet.ID)

}

func (s *Udp) pingClient(packet models.Packet) {
	addr := s.clientManger.GetClient(packet.ClientID)
	if addr == nil {
		s.clientManger.AddClient(packet.ClientID, packet.Addr)
	} else {
		s.clientManger.PingClient(packet.ClientID)
	}

	newPacket := packet
	newPacket.OpCode = OpPong
	newPacket.Length = uint16(len(packet.Payload))
	s.generateChan <- newPacket
}

func (s *Udp) registerClient(packet models.Packet) {

	s.clientManger.AddClient(packet.ClientID, packet.Addr)

	msg := fmt.Sprintf("register ack for client%d\n", int(packet.ClientID))

	newPacket := packet
	newPacket.OpCode = OpAck
	newPacket.Length = uint16(len(packet.Payload))
	newPacket.Payload = []byte(msg)

	s.generateChan <- newPacket

}

func (s *Udp) sendMessageToClient(clientID, msg string) {

	parsedClientID, err := strconv.ParseUint(clientID, 10, 16)
	if err != nil {
		fmt.Println("Invalid clientID input", err)
		return
	}

	addr := s.clientManger.GetClient(uint16(parsedClientID))

	s.generateChan <- models.Packet{OpCode: OpMessage, Payload: []byte(msg), Addr: addr}
}

func (s *Udp) handleReceiveMessage(packet models.Packet) {
	fmt.Printf("[Client%d] >> %s", packet.ClientID, packet.Payload)

	// Send Ack back to client
	newPacket := packet
	newPacket.OpCode = OpAck
	newPacket.Payload = []byte(fmt.Sprintf("Recived %s", string(packet.Payload)))
	s.generateChan <- newPacket
}

// File Management

// Senders
func (s *Udp) sendFileMeta(clientId uint16, path string) {
	file, err := os.Open(path)
	if err != nil {
		fmt.Println("falied to open file with path: ", "./message", err)
		return
	}
	// Client addr
	addr := s.clientManger.GetClient(clientId)

	// file Meta
	stat, _ := file.Stat()
	fileId := utils.GenerateTimestampID()
	fileSize := stat.Size()
	fileName := filepath.Base(path)
	doneChan := make(chan bool, 1)
	// totalChunks := uint32((fileSize + CHUNKSIZE - 1) / CHUNKSIZE)

	s.sendOutFiles.Store(fileId, &models.SendSession{File: file, FileID: fileId, FileSize: uint32(fileSize), FileName: fileName, CreatedAt: time.Now()})

	// Build Buffer [fileId 4] [fileSize 4] [filename n]
	nameBytes := []byte(fileName)
	buf := make([]byte, 4+4+len(nameBytes))
	binary.BigEndian.PutUint32(buf[0:4], fileId)
	binary.BigEndian.PutUint32(buf[4:8], uint32(fileSize))
	copy(buf[8:], nameBytes)
	// binary.BigEndian.PutUint32(buf[4:8], 0) // seq 0

	pkt := models.Packet{
		OpCode:  OpFileMeta,
		Addr:    addr,
		Payload: buf,
		Done:    doneChan,
	}

	s.generateChan <- pkt

}

func (s *Udp) handleFileReceiveSession(session *models.ReceiveSession) {
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
		fmt.Printf("Recieved From Client%d (%d/%d) seq = %d \n", chunk.ClientID, session.Received, session.Expected, chunk.Seq)

		if session.Received >= session.Expected {
			fmt.Printf("✅ Client%d File %d done (%.2f KB)\n", chunk.ClientID, chunk.FileID, float64(session.Expected)/1024)
			session.File.Close()
			close(session.ChunkChan)
			s.receivedFiles.Delete(chunk.FileID)

			return
		}
	}
}

func (s *Udp) OnMetaReceived(session *models.ReceiveSession) {
	for seq := uint32(0); seq < session.TotalChunk; seq++ {
		// Prepare request buffer
		req := make([]byte, 8)
		binary.BigEndian.PutUint32(req[0:4], session.FileID)
		binary.BigEndian.PutUint32(req[4:8], seq)
		addr := s.clientManger.GetClient(session.ClientID)
		doneChan := make(chan bool, 1)

		packet := models.Packet{
			OpCode:   OpChunkRequest,
			Payload:  req,
			Done:     doneChan,
			Addr:     addr,
			ClientID: session.ClientID,
		}

		s.generateChan <- packet

		success := <-doneChan
		if success {
			fmt.Printf("✅ Requested chunk %d acknowledged\n", seq)
		} else {
			fmt.Printf("⚠️ Chunk %d failed after timeout\n", seq)
		}
	}

}

// Reciever Handlers
func (s *Udp) handleChunkRequest(packet models.Packet) {
	// Extract Headers [fileID 4][seq 4]
	fileID := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint32(packet.Payload[4:])

	fmt.Printf("CHUNK REQ of fileID:%v , seq:%v \n", fileID, seq)

	val, exist := s.sendOutFiles.Load(fileID)
	if !exist {
		fmt.Printf("fileID %d not registered for sending", fileID)
		return
	}
	session := val.(*models.SendSession)

	// Get Chunk using File Manger
	offset := int64(seq) * int64(CHUNKSIZE)
	chunk := make([]byte, CHUNKSIZE)
	n, err := session.File.ReadAt(chunk, offset)
	if err != nil && err != io.EOF {
		fmt.Println(err)
		return
	}

	//[fileID 4] [seq 4] [payload n]
	resp := make([]byte, 8+n)
	binary.BigEndian.PutUint32(resp[0:4], fileID)
	binary.BigEndian.PutUint32(resp[4:8], seq)
	copy(resp[8:], chunk[:n])

	respPkt := packet
	respPkt.Payload = resp
	respPkt.OpCode = OpFileChunk

	s.generateChan <- respPkt
}

func (s *Udp) handleFileMeta(packet models.Packet) {
	fileId := binary.BigEndian.Uint32(packet.Payload[:4])
	fileSize := binary.BigEndian.Uint32(packet.Payload[4:8])
	fileName := string(packet.Payload[8:])
	clientID := packet.ClientID

	fmt.Printf("📦 Meta from Client%d | FileID=%d | Size=%d bytes | Name=%s\n",
		clientID, fileId, fileSize, fileName)

	chunk := models.FileChunk{
		FileID:   fileId,
		Seq:      0,
		FileSize: fileSize,
		Data:     []byte(fileName),
		ClientID: clientID,
		MetaSent: true,
	}

	s.fileChunksChan <- chunk

	// Send ACK
	ack := packet
	ack.OpCode = OpAck
	s.generateChan <- ack
}

func (s *Udp) handleFileChunk(packet models.Packet) {
	fileId := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint32(packet.Payload[4:8])
	fileData := append([]byte{}, packet.Payload[8:]...)

	chunk := models.FileChunk{
		FileID:   fileId,
		Seq:      seq,
		Data:     fileData,
		ClientID: packet.ClientID,
	}

	s.fileChunksChan <- chunk

	// Mark as chunk recieved
	ch := s.ackManger.GetAck(packet.ID)
	if ch != nil {
		ch <- true
	}

}

// func (s *Udp) handleReceiveFile(packet models.Packet) {
// 	fileId := binary.BigEndian.Uint32(packet.Payload[:4])
// 	seq := binary.BigEndian.Uint32(packet.Payload[4:8])

// 	if seq == 0 {
// 		s.handleFileMeta(packet, fileId)
// 	} else {
// 		s.handleFileChunk(packet, fileId, seq)
// 	}

//		// Always send ACK back to Client
//		outgoingPacket := packet
//		outgoingPacket.OpCode = OpAck
//		outgoingPacket.Length = uint16(len(packet.Payload))
//		s.generateChan <- outgoingPacket
//	}

func (c *Udp) cleanupSendOutFiles(ttl time.Duration) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		c.sendOutFiles.Range(func(key, value any) bool {
			session := value.(*models.SendSession)
			if now.Sub(session.CreatedAt) > ttl {
				fmt.Printf("🧹 Cleaning up expired send session (fileID=%d, name=%s)\n", session.FileID, session.FileName)
				session.File.Close()
				c.sendOutFiles.Delete(key)
			}
			return true
		})
	}
}
