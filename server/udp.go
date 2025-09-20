package server

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

type Udp struct {
	AddStr  string
	clients map[string]*net.UDPAddr
}
type serverCmd struct {
	op       byte
	clientID string
	message  string
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
			cmds <- serverCmd{op: 3, clientID: clientID, message: msg}
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
		msg := strings.TrimSpace(string(buffer[1:n]))
		clientID := msg

		cmds <- serverCmd{op: op, clientID: clientID, message: msg, addr: raddr}

	}

}

func (s *Udp) registerClient(clientID string, addr *net.UDPAddr, conn *net.UDPConn) {
	s.clients[clientID] = addr
	ack := fmt.Sprintf("ack register for client%s,addr:%s \n", clientID, addr.String())
	_, err := conn.WriteToUDP([]byte(ack), addr)
	if err != nil {
		fmt.Println("\nfailed to write date to client,err: ", err)
		return
	}
	fmt.Println("\nregistered client of addr:", addr.String())
}

func (s *Udp) pingClient(clientID string, addr *net.UDPAddr, conn *net.UDPConn) {
	s.clients[clientID] = addr
	for i := 0; i < 4; i++ {
		msg := fmt.Sprintf("ping %s time=%d", addr.String(), time.Now().Unix())
		_, err := conn.WriteToUDP([]byte(msg), addr)
		if err != nil {
			fmt.Println("\nfailed to send ping:", err)
			return
		}
		fmt.Println("\nsent:", msg)
		time.Sleep(1 * time.Second)
	}
}

func (s *Udp) sendMessageToClient(conn *net.UDPConn, clientID, message string) {
	addr := s.clients[clientID]
	if addr == nil {
		fmt.Printf("\nclient%s not found:\n", clientID)
		return
	}
	_, err := conn.WriteToUDP([]byte(message), addr)
	if err != nil {
		fmt.Printf("\nfailed to send message to %s: %v\n", addr.String(), err)
		return
	}
}

func (s *Udp) runManger(conn *net.UDPConn, cmds <-chan serverCmd) {
	s.clients = make(map[string]*net.UDPAddr)
	for cmd := range cmds {
		switch cmd.op {
		case 0: // register
			s.registerClient(cmd.clientID, cmd.addr, conn)
		case 1: // ping
			go s.pingClient(cmd.clientID, cmd.addr, conn)
		case 3: // send
			s.sendMessageToClient(conn, cmd.clientID, cmd.message)
		default:
			fmt.Printf("\nunknown op %d from %s", cmd.op, cmd.addr.String())
		}
	}
}
