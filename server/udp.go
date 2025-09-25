package server

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// OPCODE
const (
	OpRegister  byte = iota // 0
	OpPing                  // 1
	OpMessage               // 2
	OpPong                  // 3
	OpFileChunk             // 4
	OpAck                   //5
)

const CHUNKSIZE = 1015

type Udp struct {
	AddStr     string
	clients    map[string]*net.UDPAddr
	seqAckChan chan uint32
}

type serverCmd struct {
	op       byte
	clientID string
	data     []byte
	addr     *net.UDPAddr
}

func (s *Udp) StartServer() {
	// Resolve a udp addr
	udpAddr, err := net.ResolveUDPAddr("udp4", s.AddStr)
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
	fmt.Printf("%s server listening on addr %s \n", "udp", s.AddStr)

	// Run the mager of the opertaion
	cmds := make(chan serverCmd)
	s.clients = make(map[string]*net.UDPAddr)
	s.seqAckChan = make(chan uint32, 100)

	go s.runManger(connection, cmds)

	buffer := make([]byte, 1024)

	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for {
			fmt.Print("Enter clientID and message: ")
			if !scanner.Scan() {
				fmt.Println("input scanner closed")
				return
			}
			line := scanner.Text()
			parts := strings.SplitN(line, " ", 2)
			if len(parts) < 2 {
				fmt.Println("please enter: <clientID> <message>")
				continue
			}
			clientID, msg := parts[0], parts[1]
			cmds <- serverCmd{op: OpFileChunk, clientID: clientID, data: []byte(msg)}
		}
	}()

	for {
		n, raddr, err := connection.ReadFromUDP(buffer)
		if err != nil {
			fmt.Println("falied to read data, err: ", err)
			continue
		}
		if n == 0 {
			fmt.Println("no data to read")
			continue
		}

		op := buffer[0]
		payload := buffer[1:n]

		cmd := serverCmd{op: op, data: payload, addr: raddr}

		if op == OpRegister {
			cmd.clientID = string(payload)
		}

		cmds <- cmd
	}

}

func (s *Udp) registerClient(clientID string, addr *net.UDPAddr, conn *net.UDPConn) {
	s.clients[clientID] = addr
	ack := fmt.Sprintf("register ack for client%s\n", clientID)

	msg := append([]byte{OpMessage}, ([]byte(ack))...)
	_, err := conn.WriteToUDP(msg, addr)
	if err != nil {
		fmt.Println("\nfailed to write date to client,err: ", err)
		return
	}
	fmt.Println("\nregistered client of addr:", addr.String())
}

func (s *Udp) pingClient(clientID string, addr *net.UDPAddr, conn *net.UDPConn) {
	s.clients[clientID] = addr

	msg := fmt.Sprintf("pong %s time=%d", addr.String(), time.Now().Unix())
	pongMsg := append([]byte{OpPong}, ([]byte(msg))...)

	_, err := conn.WriteToUDP(pongMsg, addr)
	if err != nil {
		fmt.Println("\nfailed to send ping:", err)
		return
	}

	fmt.Println("\nsent:", msg)
	time.Sleep(1 * time.Second)

}

func (s *Udp) sendMessageToClient(conn *net.UDPConn, clientID, message string) {
	addr := s.clients[clientID]
	if addr == nil {
		fmt.Printf("\nclient%s not found:\n", clientID)
		return
	}

	msg := append([]byte{OpMessage}, ([]byte(message))...)
	_, err := conn.WriteToUDP(msg, addr)
	if err != nil {
		fmt.Printf("\nfailed to send message to %s: %v\n", addr.String(), err)
		return
	}
}

func (s *Udp) sendFileToClient(conn *net.UDPConn, clientID, filePath string) {

	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("falied to open file with path: ", filePath)
		return
	}
	defer file.Close()

	// Get file size
	stat, _ := file.Stat()
	fileSize := stat.Size()

	// Build meta
	buffer := make([]byte, CHUNKSIZE)
	seq := uint32(0)

	for {
		n, err := file.Read(buffer)
		addr := s.clients[clientID]

		if n > 0 {

			var packet []byte
			if seq == 0 {
				// First Packet
				packet = make([]byte, 1+4+4+n)
				packet[0] = byte(OpFileChunk)
				binary.BigEndian.PutUint32(packet[1:], uint32(fileSize))
				binary.BigEndian.PutUint32(packet[5:], seq)
				copy(packet[9:], buffer[:n])
			} else {
				packet = make([]byte, 1+4+n)
				packet[0] = byte(OpFileChunk)
				binary.BigEndian.PutUint32(packet[1:], seq)
				copy(packet[5:], buffer[:n])
			}

			err := s.sendChunkWithAck(conn, addr, packet, seq)
			if err != nil {
				fmt.Println(err.Error())
				return
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
	fmt.Printf("File sent successfully. Size: %.2f KB\n", float64(fileSize)/1024)
}

func (s *Udp) runManger(conn *net.UDPConn, cmds <-chan serverCmd) {

	for cmd := range cmds {
		switch cmd.op {
		case OpRegister: // register
			s.registerClient(cmd.clientID, cmd.addr, conn)
		case OpPing: // ping
			s.pingClient(cmd.clientID, cmd.addr, conn)
		case OpMessage: // send
			s.sendMessageToClient(conn, cmd.clientID, string(cmd.data))
		case OpFileChunk: // send
			go s.sendFileToClient(conn, cmd.clientID, "./message.txt")
		case OpAck:
			ackType := cmd.data[0]
			switch ackType {
			case OpRegister: // register
				fmt.Printf("Register ack meesage from %s: %s\n", cmd.clientID, string(cmd.data))
			case OpPing: // ping
				fmt.Printf("Ping ack meesage from %s: %s\n", cmd.clientID, string(cmd.data))
			case OpMessage: // send
				fmt.Printf("Message ack meesage from %s: %s\n", cmd.clientID, string(cmd.data))
			case OpFileChunk:
				seq := binary.BigEndian.Uint32(cmd.data[1:])
				s.seqAckChan <- seq
			}
		// case
		default:
			fmt.Printf("\nunknown op %d from %s", cmd.op, cmd.addr.String())
		}
	}
}

// func (s *Udp) sendDataToClient(conn *net.UDPConn, clientAddr *net.UDPAddr, data []byte) {
// 	_, err := conn.WriteToUDP(data, clientAddr)
// 	if err != nil {
// 		fmt.Printf("\nfailed to send message to %s: %v\n", clientAddr.String(), err)
// 		return
// 	}
// }

func (s *Udp) sendChunkWithAck(conn *net.UDPConn, clientAddr *net.UDPAddr, packet []byte, seq uint32) error {
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {

		// Send the chunk
		_, err := conn.WriteToUDP(packet, clientAddr)
		if err != nil {
			return fmt.Errorf("failed to send chunk:", err)
		}

		// fmt.Println("")
		// Wait for the ack
		select {
		case ackSeq := <-s.seqAckChan:
			if ackSeq == seq {
				// Success Ack
				return nil
			}
		case <-time.After(1 * time.Second):
			fmt.Printf("Timeout waiting for ACK %d, retrying...\n", seq)
		}
	}
	return fmt.Errorf("failed to deliver chunk %d after retries\n", seq)
}
