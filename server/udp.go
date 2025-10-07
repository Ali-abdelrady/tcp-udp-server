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

	clientManger workers.ClientManager
	ackManger    *workers.AckManager

	// pendingAck   map[uint32]chan bool
	// pendingMutex sync.Mutex
}

const (
	BUFFER_SIZE = 65507
	CHUNKSIZE   = 40000
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
	s.clientManger = *workers.NewClientManager()
	s.ackManger = workers.NewAckManager()
	s.trackingChan = make(chan models.Packet, 200)
	// s.pendingAck = make(map[uint32]chan bool)

	// Run Workers
	for i := 0; i < 4; i++ {
		go s.parserWorker()
		go s.writeWorker(connection)
		go s.generatorWorker()
		go s.trackingWorker()
	}

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
		}

		switch packet.OpCode {
		case OpAck:
			s.handleAck(packet)
		case OpPing:
			s.pingClient(packet)
		case OpRegister:
			s.registerClient(packet)
			// case OpDownload:
			// 	s.downloadFile(packet)
		}
	}
}

func (s *Udp) generatorWorker() {
	for {
		packet := <-s.generateChan

		// [opcode] [packetId] [payload n]
		var packetID uint32

		if packet.OpCode == OpAck {
			// use client packetID
			packetID = packet.ID
		} else {
			// Generate New one
			packetID = utils.GenerateTimestampID()
		}

		// Packet [opcode 1] [packetId 4] [clientId 2] [payload n]
		buf := make([]byte, 1+4+2+len(packet.Payload))
		buf[0] = packet.OpCode
		binary.BigEndian.PutUint32(buf[1:5], packetID)
		binary.BigEndian.PutUint16(buf[5:7], packet.ClientID)
		copy(buf[7:], packet.Payload)

		// fmt.Printf("Sended Packet ID : %v\n", packetID)
		outgoingPacket := models.Packet{Payload: buf, Addr: packet.Addr, ID: packetID, Done: packet.Done}

		if packet.OpCode == OpAck {
			// use client packetID
			s.writeChan <- outgoingPacket
		} else {
			// Generate New one
			s.trackingChan <- outgoingPacket
		}

	}
}

func (s *Udp) startInteractiveCommandInput() {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("Enter <clientId> <operation> <message/filepath(optional)>: ")
		if !scanner.Scan() {
			fmt.Println("input scanner closed")
			return
		}

		line := scanner.Text()
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			fmt.Println("please enter: <clientId> <operation> <message/filepath(optional)>")
			continue
		}

		clientID := parts[0]
		operation := parts[1]
		var payload string
		if len(parts) == 3 {
			payload = parts[2]
		}

		switch operation {
		case "message":
			if payload == "" {
				fmt.Println("please provide a message")
				continue
			}
			s.sendMessageToClient(clientID, payload)

		case "file":
			// if payload == "" {
			// 	fmt.Println("please provide a file path")
			// 	continue
			// }
			parsedClientID, err := strconv.ParseUint(clientID, 10, 16)
			if err != nil {
				fmt.Println("Invalid clientID input", err)
				return
			}
			s.sendFileToClient(uint16(parsedClientID), "./image.jpg")

		default:
			fmt.Printf("unknown operation: %s\n", operation)
		}
	}
}

func (s *Udp) pingClient(packet models.Packet) {
	s.clientManger.AddClient(packet.ClientID, packet.Addr)

	msg := fmt.Sprintf("Ping ack for client%d", int(packet.ClientID))

	newPacket := packet
	newPacket.OpCode = OpAck
	newPacket.Payload = []byte(msg)

	s.generateChan <- newPacket
}

func (s *Udp) registerClient(packet models.Packet) {

	s.clientManger.AddClient(packet.ClientID, packet.Addr)

	msg := fmt.Sprintf("register ack for client%d\n", int(packet.ClientID))

	newPacket := packet
	newPacket.OpCode = OpAck
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
		time.Sleep(2 * time.Millisecond) // 👈 helps throttle sending rate

		select {
		case <-ackCh:
			if packet.Done != nil {
				packet.Done <- true
			}
			fmt.Printf("ACK received for packet %d\n", packet.ID)
			return nil
		case <-time.After(1 * time.Second):
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
	fmt.Printf("File sent successfully. Size: %.2f KB\n", float64(fileSize))
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
