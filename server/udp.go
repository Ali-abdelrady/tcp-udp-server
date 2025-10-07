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
	"sync"
	"time"
)

type Udp struct {
	Port         string
	parserChan   chan models.RawPacket
	writeChan    chan models.Packet
	generateChan chan models.Packet
	trackingChan chan models.Packet

	clientManger workers.ClientManager
	pendingAck   map[uint32]chan bool
	pendingMutex sync.Mutex
}

const (
	BUFFER_SIZE = 65507
	CHUNKSIZE   = 60000
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
	s.trackingChan = make(chan models.Packet, 100)
	s.pendingAck = make(map[uint32]chan bool)

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

		s.parserChan <- models.RawPacket{Data: buffer[:n], Addr: addr}
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

		// Packet [opcode 1] [packetId 4] [payload n]
		buf := make([]byte, 1+4+len(packet.Payload))
		buf[0] = packet.OpCode
		binary.BigEndian.PutUint32(buf[1:5], packetID)
		copy(buf[5:], packet.Payload)

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

	s.pendingMutex.Lock()
	ch, ok := s.pendingAck[packet.ID]
	s.pendingMutex.Unlock()

	if ok {
		ch <- true
	}
}

func (s *Udp) sendWithAck(packet models.Packet) error {
	retries := 3

	internalAck := make(chan bool, 1)

	s.pendingMutex.Lock()
	s.pendingAck[packet.ID] = internalAck
	s.pendingMutex.Unlock()

	// fmt.Println("pendingAck:", s.pendingAck)
	defer func() {
		s.pendingMutex.Lock()
		delete(s.pendingAck, packet.ID)
		s.pendingMutex.Unlock()
	}()

	for i := 0; i < retries; i++ {

		s.writeChan <- packet

		select {
		case <-internalAck:
			if packet.Done != nil {
				packet.Done <- true
			}
			fmt.Printf("ACK received for packet %d\n", packet.ID)
			return nil
		case <-time.After(4 * time.Second):
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
	doneChan := make(chan bool)

	for {
		n, err := file.Read(buffer)

		if n > 0 {

			offset := 4

			if seq == 0 {
				offset = 8
			}

			// allocate enough space for header + data
			pkt := make([]byte, offset+n)
			binary.BigEndian.PutUint32(pkt[:4], seq)

			if seq == 0 {
				binary.BigEndian.PutUint32(pkt[4:8], uint32(fileSize))
			}

			// chunk [seq 4] [fileSize 4] [data n]
			copy(pkt[offset:], buffer[:n])

			if seq == 0 {
				s.generateChan <- models.Packet{OpCode: OpFileChunk, Payload: pkt, Addr: addr, ClientID: clientId, Done: doneChan}
				if !<-doneChan {
					break
				}
			} else {
				s.generateChan <- models.Packet{OpCode: OpFileChunk, Payload: pkt, Addr: addr, ClientID: clientId}
			}
			// sendedSeq = append(sendedSeq, seq)
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
