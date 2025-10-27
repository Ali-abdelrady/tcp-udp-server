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

const (
	BUFFER_SIZE = 65507
	CHUNKSIZE   = 1400
	HEADER_SIZE = 7 // [opcode 1] [packetID 4] [ClientID 2]
	alpha       = 0.125
	beta        = 0.25
)

// OPCODES
const (
	OpRegister            byte = iota // 0
	OpPing                            // 1
	OpMessage                         // 2
	OpPong                            // 3
	OpFileChunk                       // 4
	OpAck                             //5
	OpFileMeta                        //6
	OpChunkRequest                    //7
	OpChunkStatusRequest              // 8
	OpChunkStatusResponse             // 9
)

type Udp struct {
	Port string

	parserChan     chan models.RawPacket
	writeChan      chan models.Packet
	generateChan   chan models.Packet
	ackChan        chan models.Packet
	fileMetaChan   chan models.FileMeta
	fileChunksChan chan models.FileChunk

	receivedFiles  sync.Map // map[uint32]*models.ReceiveSession
	sendOutFiles   sync.Map // map[uint32]*models.SendSession
	pendingPackets sync.Map

	// fileManger   *workers.FileManager
	clientManger workers.ClientManager
	// ackManger    *workers.AckManager

	//? RTT & Retransmission time out
	smoothedRTT time.Duration
	rttVar      time.Duration
	rto         time.Duration

	//? Congestion Control
	congestionWindow    int // Congestion Window
	slowStartThreshold  int // start slow threshold
	maxCongestionWindow int
	// bytesInFlight       float64
	ackCount int
}

func NewUdp(port string) *Udp {
	return &Udp{
		Port:           port,
		parserChan:     make(chan models.RawPacket, 50),
		writeChan:      make(chan models.Packet, 50),
		generateChan:   make(chan models.Packet, 50),
		ackChan:        make(chan models.Packet, 200),
		fileChunksChan: make(chan models.FileChunk, 100),
		fileMetaChan:   make(chan models.FileMeta, 50),
		clientManger:   *workers.NewClientManager(),
		// ackManger:      workers.NewAckManager(),
	}
}

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

	s.clientManger = *workers.NewClientManager()
	// s.ackManger = workers.NewAckManager()

	// Run Workers

	go s.parserWorker()
	go s.generatorWorker()
	go s.fileMetaWorker()
	go s.fileChunkWorker()
	go s.retransmissionWorker()
	go s.ackListener()
	go s.writeWorker(connection)
	go s.startInteractiveCommandInput()

	go s.cleanupSendOutFiles(10 * time.Minute)

	// 🔹 Defer log for graceful shutdown
	defer utils.PrintApiLog("Server Shutdown")
	s.setupGracfulShutdown()

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
	for {

		pkt := <-s.writeChan

		if pkt.Addr == nil {
			fmt.Printf("⚠️ [writeWorker] packet ID=%d has nil address, skipping\n", pkt.ID)
			continue
		}

		_, err := conn.WriteToUDP(pkt.Payload, pkt.Addr)
		if err != nil {
			fmt.Println("failed to send packet")
			continue
		}

		// ✅ mark actual send time
		if pkt.Trackable {
			pp := &models.PendingPacket{
				Packet:   pkt,
				SendTime: time.Now(),
				Retries:  0,
				AckChan:  make(chan bool, 1),
			}
			s.pendingPackets.Store(pkt.ID, pp)
		}

		// pacing
		interval := 10 * time.Millisecond
		if s.smoothedRTT > 0 && s.congestionWindow > 0 {
			pacingRate := float64(s.congestionWindow*CHUNKSIZE) / s.smoothedRTT.Seconds()
			interval = time.Duration(float64(CHUNKSIZE) / pacingRate * float64(time.Second))
		} else {
			interval = 10 * time.Millisecond
		}

		time.Sleep(interval)
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
			s.ackChan <- packet
		case OpPing:
			s.onPingReceived(packet)
		case OpRegister:
			s.onRegisterReceived(packet)
		case OpMessage:
			s.onMessageReceived(packet)
		case OpFileMeta:
			s.onFileMetaReceived(packet)
		case OpFileChunk:
			s.onFileChunkReceived(packet)
		case OpChunkRequest:
			s.onChunkRequestReceived(packet)
		case OpChunkStatusRequest:
			s.onChunkStatusRequestReceived(packet)
		case OpChunkStatusResponse:
			s.onChunkStatusResponseReceived(packet)

		}
	}
}

func (s *Udp) generatorWorker() {
	for {
		packet := <-s.generateChan

		var packetID uint32
		isUnreliable := packet.OpCode == OpAck || packet.OpCode == OpPong || packet.OpCode == OpChunkStatusResponse

		if isUnreliable {
			packetID = packet.ID
		} else {
			packetID = utils.GenerateTimestampID()
		}

		finalPayload := s.buildPacketPayload(packet, packetID)

		outgoingPacket := models.Packet{
			Payload:   finalPayload,
			Addr:      packet.Addr,
			ID:        packetID,
			Done:      packet.Done,
			Trackable: !isUnreliable,
		}

		s.writeChan <- outgoingPacket

	}
}

func (s *Udp) buildPacketPayload(packet models.Packet, packetID uint32) []byte {
	// Format: [opcode 1] [packetId 4] [Length 2] [clientId 2] [payload n]
	buf := make([]byte, 1+4+2+2+len(packet.Payload))
	buf[0] = packet.OpCode
	binary.BigEndian.PutUint32(buf[1:5], packetID)
	binary.BigEndian.PutUint16(buf[5:7], uint16(len(buf)))
	binary.BigEndian.PutUint16(buf[7:9], packet.ClientID)
	copy(buf[9:], packet.Payload)
	return buf
}

func (s *Udp) ackListener() {
	for {
		ackPkt := <-s.ackChan

		// Find pending packet
		if value, ok := s.pendingPackets.LoadAndDelete(ackPkt.ID); ok {

			pp := value.(*models.PendingPacket)
			pp.AckChan <- true
			if pp.Packet.Done != nil {
				pp.Packet.Done <- true
			}

			// --- RTT update ---
			rtt := time.Since(pp.SendTime)
			s.updateRTT(rtt)

			// --- Congestion Control ---
			if s.congestionWindow < s.slowStartThreshold {
				// Slow start: exponential growth
				s.congestionWindow *= 2
				fmt.Printf("🚀 Slow Start: cwnd doubled to %d\n", s.congestionWindow)
			} else {
				// Congestion avoidance: linear growth (1 packet per RTT)
				s.ackCount++
				if s.ackCount >= s.congestionWindow {
					s.congestionWindow++
					s.ackCount = 0
					fmt.Printf("📈 Linear Growth: cwnd increased to %d\n", s.congestionWindow)
				}
			}

			// Clamp cwnd to prevent runaway growth
			if s.congestionWindow > s.maxCongestionWindow {
				s.congestionWindow = s.maxCongestionWindow
			}

			// --- Remove from pending ---

			// s.pendingPackets.Delete(ackPkt.ID)

			// --- Logging ---
			fmt.Printf(
				"✅ ACK %v | RTT: %v | Smoothed: %v | Var: %v | RTO: %v | cwnd: %d | ssthresh: %d\n",
				pp.Packet.ID,
				rtt,
				s.smoothedRTT,
				s.rttVar,
				s.rto,
				s.congestionWindow,
				s.slowStartThreshold,
			)
		}
	}
}

func (s *Udp) retransmissionWorker() {
	maxRetries := 3
	ticker := time.NewTicker(200 * time.Millisecond) // check more frequently

	for range ticker.C {
		now := time.Now()

		s.pendingPackets.Range(func(key, value any) bool {
			pp := value.(*models.PendingPacket)

			// Get latest adaptive RTO
			baseTimeout := s.rto * 5
			if baseTimeout == 0 {
				baseTimeout = 500 * time.Millisecond // default before any RTT samples
			}

			nextRetryDelay := baseTimeout * time.Duration(1<<pp.Retries)

			// Check if timeout expired
			if now.Sub(pp.SendTime) > nextRetryDelay && pp.Retries < maxRetries {
				fmt.Printf("⏱️ Retrying packet %d (attempt %d, timeout=%v)\n",
					pp.Packet.ID, pp.Retries+1, baseTimeout)

				// Retransmit
				s.writeChan <- pp.Packet
				pp.SendTime = now
				pp.Retries++

			} else if pp.Retries >= maxRetries {
				fmt.Printf("❌ Packet %d failed after %d retries\n", pp.Packet.ID, pp.Retries)
				s.pendingPackets.Delete(pp.Packet.ID)

				// Congestion reaction
				s.slowStartThreshold = max(2, s.congestionWindow/2)
				s.congestionWindow = 1

				if pp.Packet.Done != nil {
					pp.Packet.Done <- false
				}
			}

			return true
		})
	}
}

func (s *Udp) fileMetaWorker() {
	for meta := range s.fileMetaChan {
		s.createSessionFromMeta(meta)
	}
}

// Handles incoming file chunks (writes them to disk)

func (s *Udp) fileChunkWorker() {
	for chunk := range s.fileChunksChan {
		s.appendFileChunk(chunk)
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

// --------Parser Handlers--------

func (s *Udp) onPingReceived(packet models.Packet) {
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

func (s *Udp) onRegisterReceived(packet models.Packet) {

	s.clientManger.AddClient(packet.ClientID, packet.Addr)

	msg := fmt.Sprintf("register ack for client%d\n", int(packet.ClientID))

	newPacket := packet
	newPacket.OpCode = OpAck
	newPacket.Length = uint16(len(packet.Payload))
	newPacket.Payload = []byte(msg)

	s.generateChan <- newPacket

}

func (s *Udp) onMessageReceived(packet models.Packet) {
	fmt.Printf("[Client%d] >> %s", packet.ClientID, packet.Payload)

	// Send Ack back to client
	newPacket := packet
	newPacket.OpCode = OpAck
	newPacket.Payload = []byte(fmt.Sprintf("Recived %s", string(packet.Payload)))
	s.generateChan <- newPacket
}

func (s *Udp) onChunkRequestReceived(packet models.Packet) {
	// Extract Headers [fileID 4][seq 2]
	fileID := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint16(packet.Payload[4:])

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

	if session.SentChunks == nil {
		session.SentChunks = make(map[uint16]time.Time)
	}
	session.SentChunks[seq] = time.Now()

	//[fileID 4] [seq 2] [payload n]
	resp := make([]byte, 6+n)
	binary.BigEndian.PutUint32(resp[0:4], fileID)
	binary.BigEndian.PutUint16(resp[4:6], seq)
	copy(resp[6:], chunk[:n])

	respPkt := packet
	respPkt.Payload = resp
	respPkt.OpCode = OpFileChunk

	s.generateChan <- respPkt
}

func (s *Udp) onFileMetaReceived(packet models.Packet) {
	// [fileId 4] [fileSize 4] [ChunkSize 2] [filename n]

	fileId := binary.BigEndian.Uint32(packet.Payload[:4])
	fileSize := binary.BigEndian.Uint32(packet.Payload[4:8])
	chunkSize := binary.BigEndian.Uint16(packet.Payload[8:10])
	fileName := string(packet.Payload[10:])
	clientID := packet.ClientID

	fmt.Printf("📦 Meta from Client%d | FileID=%d | Size=%d bytes | Name=%s | ChunkSize=%d \n",
		clientID, fileId, fileSize, fileName, chunkSize)

	meta := models.FileMeta{
		FileID:    fileId,
		FileSize:  fileSize,
		FileName:  []byte(fileName),
		ChunkSize: chunkSize,
		ClientID:  clientID,
	}

	// Send Meta to fileManger
	s.fileMetaChan <- meta

	// Sends Ack
	ack := packet
	ack.OpCode = OpAck
	s.generateChan <- ack
}

func (s *Udp) onFileChunkReceived(packet models.Packet) {
	// [fileId 4] [seq 4] [filename n]

	fileId := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint16(packet.Payload[4:6])
	fileData := append([]byte{}, packet.Payload[6:]...)

	chunk := models.FileChunk{
		FileID:   fileId,
		Seq:      seq,
		Data:     fileData,
		ClientID: packet.ClientID,
	}

	s.fileChunksChan <- chunk

	newPacket := packet
	newPacket.OpCode = OpAck
	newPacket.Payload = []byte{}
	s.generateChan <- newPacket

	// Mark as chunk recieved
	// ch := s.ackManger.GetAck(packet.ID)
	// if ch != nil {
	// 	ch <- true
	// }
}

func (s *Udp) onChunkStatusResponseReceived(packet models.Packet) {
	fileID := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint16(packet.Payload[4:6])
	status := packet.Payload[6]

	// Send ACk
	s.ackChan <- packet
	// ch := s.ackManger.GetAck(packet.ID)
	// if ch != nil {
	// 	ch <- true
	// }

	val, ok := s.receivedFiles.Load(fileID)
	if !ok {
		fmt.Printf("⚠️ No active session found for FileID=%d\n", fileID)
		return
	}

	session := val.(*models.ReceiveSession)

	// Push response into the session’s AckChan
	select {
	case session.AckChan <- models.AckResponse{Seq: seq, Status: status}:
		// sent successfully to listener
	default:
		fmt.Printf("⚠️ AckChan full, dropping status response for FileID=%d Seq=%d\n", fileID, seq)
	}
}

func (s *Udp) onChunkStatusRequestReceived(packet models.Packet) {
	fileID := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint16(packet.Payload[4:6])

	status := byte(3) // Default: Not Found

	if val, ok := s.sendOutFiles.Load(fileID); ok {
		session := val.(*models.SendSession)

		// Ensure map exists
		if session.SentChunks == nil {
			session.SentChunks = make(map[uint16]time.Time)
		}

		if _, exists := session.SentChunks[seq]; exists {
			status = 2 // Already Sent
		} else {
			status = 0 // Not Sent
		}
	}

	resp := make([]byte, 7)
	binary.BigEndian.PutUint32(resp[0:4], fileID)
	binary.BigEndian.PutUint16(resp[4:6], seq)
	resp[6] = status

	response := packet
	response.OpCode = OpChunkStatusResponse
	response.Payload = resp

	s.generateChan <- response
}

// ---------Senders---------

func (s *Udp) sendMessageToClient(clientID, msg string) {

	parsedClientID, err := strconv.ParseUint(clientID, 10, 16)
	if err != nil {
		fmt.Println("Invalid clientID input", err)
		return
	}

	addr := s.clientManger.GetClient(uint16(parsedClientID))

	s.generateChan <- models.Packet{OpCode: OpMessage, Payload: []byte(msg), Addr: addr}
}

func (s *Udp) sendFileMeta(clientId uint16, path string) {
	file, err := os.Open(path)
	if err != nil {
		fmt.Println("falied to open file with path: ", "./message", err)
		return
	}
	defer file.Close()

	// Client addr
	addr := s.clientManger.GetClient(clientId)

	// file Meta
	stat, _ := file.Stat()
	fileId := utils.GenerateTimestampID()
	fileSize := stat.Size()
	fileName := filepath.Base(path)
	doneChan := make(chan bool, 1)

	// Store the sendout files
	s.sendOutFiles.Store(fileId, &models.SendSession{File: file, FileID: fileId, FileSize: uint32(fileSize), FileName: fileName, CreatedAt: time.Now()})

	// Build Buffer [fileId 4] [fileSize 4] [ChunkSize 2] [filename n]
	nameBytes := []byte(fileName)
	buf := make([]byte, 4+4+2+len(nameBytes))
	binary.BigEndian.PutUint32(buf[0:4], fileId)
	binary.BigEndian.PutUint32(buf[4:8], uint32(fileSize))
	binary.BigEndian.PutUint16(buf[8:10], uint16(CHUNKSIZE))
	copy(buf[10:], nameBytes)

	pkt := models.Packet{
		OpCode:  OpFileMeta,
		Addr:    addr,
		Payload: buf,
		Done:    doneChan,
	}

	s.generateChan <- pkt

	if !<-doneChan {
		fmt.Println("Didn't recive the meta ack")
		return
	}

	time.Sleep(1 * time.Microsecond)

	seq := uint16(0)
	chunk := make([]byte, CHUNKSIZE)

	for {
		n, err := file.Read(chunk)

		if n > 0 {

			//  [fileId 4] [seq 2]
			payload := make([]byte, 6+n)
			binary.BigEndian.PutUint32(payload[:4], fileId)
			binary.BigEndian.PutUint16(payload[4:6], seq)
			copy(payload[6:], chunk[:n])

			s.generateChan <- models.Packet{OpCode: OpFileChunk, Payload: payload, ClientID: clientId, Addr: addr}

			seq++
		}

		if err == io.EOF {
			break
		}

		if err != nil {
			fmt.Println("failed to read files")
			return
		}
	}
}

func (s *Udp) sendChunkStatusRequest(reqPacket models.Packet) {

	fileID := binary.BigEndian.Uint32(reqPacket.Payload[:4])
	seq := binary.BigEndian.Uint16(reqPacket.Payload[4:6])
	clientID := reqPacket.ClientID
	addr := reqPacket.Addr

	fmt.Printf("📡 Sending status request for FileID=%d, Seq=%d\n", fileID, seq)

	// Build payload: [fileID 4][seq 2]
	buf := make([]byte, 6)
	binary.BigEndian.PutUint32(buf[0:4], fileID)
	binary.BigEndian.PutUint16(buf[4:6], seq)

	statusReqPkt := models.Packet{
		OpCode:   OpChunkStatusRequest,
		Payload:  buf,
		ClientID: clientID,
		Addr:     addr,
	}

	s.generateChan <- statusReqPkt
}

func (s *Udp) RequestFileChunks(session *models.ReceiveSession) {
	for seq := uint16(0); seq < session.TotalChunk; seq++ {
		if session.Chunks[seq] {
			continue // already received
		}

		fileID := session.FileID
		addr := s.clientManger.GetClient(session.ClientID)

		fmt.Printf("Requesting chunk %d...\n", seq)

		req := make([]byte, 6)
		binary.BigEndian.PutUint32(req[0:4], fileID)
		binary.BigEndian.PutUint16(req[4:6], seq)
		packet := models.Packet{
			OpCode:   OpChunkRequest,
			Payload:  req,
			Addr:     addr,
			ClientID: session.ClientID,
		}

		s.generateChan <- packet

		timeout := time.NewTimer(5 * time.Second)
		var resp models.AckResponse
		select {
		case resp = <-session.AckChan:
			// response received
			fmt.Println("Recieved Chuck ACk")
		case <-timeout.C:
			fmt.Printf("⏱ Timeout waiting for chunk %d, sending status query...\n", seq)
			s.sendChunkStatusRequest(packet)
			// Wait again for the status response
			select {
			case resp = <-session.AckChan:
			case <-time.After(3 * time.Second):
				fmt.Printf("⚠️ No status response for chunk %d, aborting.\n", seq)
				return
			}
		}

		timeout.Stop()

		switch resp.Status {
		case 200:
			fmt.Printf("Chunk %d received OK\n", seq)
			continue
		case 0: // NotSent
			fmt.Printf("Chunk %d not sent yet, retrying...\n", seq)
			seq-- // retry
			continue
		case 1: // InProgress
			fmt.Printf("Chunk %d still being processed, waiting...\n", seq)
			time.Sleep(2 * time.Second)
			seq--
			continue
		case 2: // AlreadySent
			fmt.Printf("Chunk %d already sent, retrying...\n", seq)
			seq--
			continue
		case 3: // NotFound
			fmt.Printf("Chunk %d not found, aborting.\n", seq)
			return
		default:
			fmt.Printf("Unknown status %d for chunk %d\n", resp.Status, seq)
			return
		}
	}
}

//------File Manager Handlers--------

func (s *Udp) createSessionFromMeta(meta models.FileMeta) {

	// Check if file already exists
	_, exists := s.receivedFiles.Load(meta.FileID)
	if exists {
		fmt.Println("Meta already exists for file:", meta.FileName)
		return
	}

	// Start Creating New File
	wd, err := os.Getwd()
	if err != nil {
		fmt.Println("Error getting working directory:", err)
		return
	}

	filePath := filepath.Join(wd, fmt.Sprintf("client%d_%s", meta.ClientID, meta.FileName))
	file, err := os.Create(filePath)
	if err != nil {
		fmt.Println("Error creating file:", err)
		return
	}

	// Start new session for the this file
	session := &models.ReceiveSession{
		FileMeta:   meta,
		File:       file,
		Received:   0,
		Chunks:     make(map[uint16]bool),
		TotalChunk: uint16((meta.FileSize + uint32(meta.ChunkSize) - 1) / uint32(meta.ChunkSize)),
		AckChan:    make(chan models.AckResponse, 10),
	}

	// Store the session
	s.receivedFiles.Store(meta.FileID, session)

	// Start Req for file chunks
	// s.RequestFileChunks(session)
}

func (s *Udp) appendFileChunk(chunk models.FileChunk) {
	val, exists := s.receivedFiles.Load(chunk.FileID)
	if !exists {
		fmt.Printf("Received chunk for unknown file %d (waiting for meta)\n", chunk.FileID)
		return
	}

	session := val.(*models.ReceiveSession)

	// Check for duplicates
	if session.Chunks[chunk.Seq] {
		fmt.Printf("⚠ Duplicate Seq = %d ignored \n", chunk.Seq)
		return
	}

	//
	// session.AckChan <- models.AckResponse{
	// 	Seq:    chunk.Seq,
	// 	Status: 200, // ReceivedOK
	// }

	session.Chunks[chunk.Seq] = true

	offset := int64(chunk.Seq) * int64(session.ChunkSize)
	session.File.WriteAt(chunk.Data, offset)
	session.Received++

	fmt.Printf("Recieved From Client%d (%d/%d) seq = %d \n", chunk.ClientID, session.Received, session.TotalChunk, chunk.Seq)

	if session.Received > session.TotalChunk {
		fmt.Printf("✅ Client%d File %d done \n", chunk.ClientID, chunk.FileID)
		session.File.Close()
		// close(session.ChunkChan)
		s.receivedFiles.Delete(chunk.FileID)

		return
	}
}

// ---------- Additional Functions----------
func (s *Udp) cleanupSendOutFiles(ttl time.Duration) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		s.sendOutFiles.Range(func(key, value any) bool {
			session := value.(*models.SendSession)
			if now.Sub(session.CreatedAt) > ttl {
				fmt.Printf("🧹 Cleaning up expired send session (fileID=%d, name=%s)\n", session.FileID, session.FileName)
				session.File.Close()
				s.sendOutFiles.Delete(key)
			}
			return true
		})
	}
}

func (s *Udp) setupGracfulShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n🛑 Shutting down server gracefully...")
		// fmt.Println("AllocationCnt: ", s.allocationCnt)
		utils.PrintApiLog("Server Exit")
		os.Exit(0)
	}()
}

func (s *Udp) updateRTT(rtt time.Duration) {
	// Update the RTT
	if s.smoothedRTT == 0 {
		s.smoothedRTT = rtt
		s.rttVar = rtt / 2
	} else {
		rttDiff := s.smoothedRTT - rtt
		if rttDiff < 0 {
			rttDiff = -rttDiff
		}

		s.rttVar = time.Duration((1-beta)*float64(s.rttVar) + beta*float64(rttDiff))
		s.smoothedRTT = time.Duration((1-alpha)*float64(s.smoothedRTT) + alpha*float64(rtt))
	}

	s.rto = s.smoothedRTT + 4*s.rttVar

}
