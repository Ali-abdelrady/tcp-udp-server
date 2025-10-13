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
	"strconv"
	"strings"
	"time"
)

type Udp struct {
	Port         string
	parserChan   chan models.RawPacket
	writeChan    chan models.Packet
	generateChan chan models.Packet
	trackingChan chan models.Packet

	fileManger   *workers.FileManger
	clientManger workers.ClientManager
	ackManger    *workers.AckManager
}

const (
	BUFFER_SIZE = 65507
	CHUNKSIZE   = 10000
)

// OPCODES
const (
	OpRegister  byte = iota // 0
	OpPing                  // 1
	OpMessage               // 2
	OpPong                  // 3
	OpFileChunk             // 4
	OpAck                   //5
	OpDownload              //6
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

	// Initialize Channel and workers
	s.writeChan = make(chan models.Packet, 50)
	s.parserChan = make(chan models.RawPacket, 50)
	s.generateChan = make(chan models.Packet, 50)
	s.trackingChan = make(chan models.Packet, 200)

	s.clientManger = *workers.NewClientManager()
	s.ackManger = workers.NewAckManager()
	s.fileManger = workers.NewFileManger()

	// s.pendingAck = make(map[uint32]chan bool)

	// Run Workers
	for i := 0; i < 3; i++ {
		go s.parserWorker()
		go s.generatorWorker()
		go s.trackingWorker()
	}
	go s.writeWorker(connection)
	go s.startInteractiveCommandInput()

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
		case OpFileChunk:
			s.handleReceiveFile(packet)
		}
	}
}

func (s *Udp) generatorWorker() {
	for {
		packet := <-s.generateChan

		var packetID uint32
		if packet.OpCode == OpAck || packet.OpCode == OpPong {
			packetID = packet.ID
		} else {
			packetID = utils.GenerateTimestampID()
		}

		buf := make([]byte, 1+4+2+2+len(packet.Payload)) // fixed missing +2
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

		switch packet.OpCode {
		case OpAck, OpPong:
			// Forward ACK/PONG without creating new buffer
			fmt.Printf("Send Ack To PacketID =  %d , Size = %d\n",packetID,len(buf))
			s.writeChan <- outgoing
		default:
			// Packet [opcode 1] [packetId 4] [size 2] [clientId 2] [payload n]
			s.trackingChan <- outgoing
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

			s.sendFileToClient(uint16(clientID), filepath)

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

func (s *Udp) trackingWorker() {
	for packet := range s.trackingChan {
		err := s.sendWithAck(packet)
		if err != nil {
			fmt.Printf("[Tracking] Failed to deliver packet %d after retries\n", packet.ID)
		}
	}
}

func (s *Udp) sendFileToClient(clientId uint16, filepath string) {
	// sendedSeq := []uint32{}

	file, err := os.Open(filepath)
	if err != nil {
		fmt.Println("falied to open file with path: ", "./message")
		return
	}
	defer file.Close()

	// Get file size
	stat, _ := file.Stat()
	fileSize := stat.Size()

	// Build meta
	buffer := make([]byte, CHUNKSIZE)
	seq := uint32(0)
	addr := s.clientManger.GetClient(clientId)
	doneChan := make(chan bool, 1)
	fileId := utils.GenerateTimestampID()

	// Build Meta Packet and send it waiting for ack

	for {
		n, err := file.Read(buffer)

		if n > 0 {

			// [fileId 4] [seq 4] = 8 //? for seq != 0
			offset := 8

			if seq == 0 {
				offset = 12
			}

			// allocate enough space for header + data
			pkt := make([]byte, offset+n)
			binary.BigEndian.PutUint32(pkt[:4], fileId)
			binary.BigEndian.PutUint32(pkt[4:8], seq)

			if seq == 0 {
				binary.BigEndian.PutUint32(pkt[8:12], uint32(fileSize))
			}

			// chunk [fileId 4] [seq 4] [fileSize 4] [data n]
			copy(pkt[offset:], buffer[:n])

			if seq == 0 {
				s.generateChan <- models.Packet{OpCode: OpFileChunk, Payload: pkt, Addr: addr, ClientID: clientId, Done: doneChan}
				if !<-doneChan {
					break
				}
			} else {
				s.generateChan <- models.Packet{OpCode: OpFileChunk, Payload: pkt, Addr: addr, ClientID: clientId}
			}

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
	// fmt.Println("Seq:", sendedSeq)
	// fmt.Printf("File sent successfully. Size: %.2f KB\n", float64(fileSize))
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

func (s *Udp) handleReceiveFile(packet models.Packet) {
	fileId := binary.BigEndian.Uint32(packet.Payload[:4])
	seq := binary.BigEndian.Uint32(packet.Payload[4:8])
	var fileData []byte
	var fileSize uint32

	if seq == 0 {
		fileSize = binary.BigEndian.Uint32(packet.Payload[8:12])
		fileData = make([]byte, len(packet.Payload[12:]))
		copy(fileData, packet.Payload[12:])
	} else {
		fileData = make([]byte, len(packet.Payload[8:]))
		copy(fileData, packet.Payload[8:])
	}

	chunk := workers.FileChunk{
		FileID:   fileId,
		Seq:      seq,
		FileSize: fileSize,
		Data:     fileData,
	}

	s.fileManger.Ops <- chunk

	//  Always send ACK back to Client
	outgoingPacket := packet
	outgoingPacket.OpCode = OpAck
	outgoingPacket.Length = uint16(len(packet.Payload))
	// outgoingPacket.Payload = []byte{}
	s.generateChan <- outgoingPacket
}
