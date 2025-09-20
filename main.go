package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Missing args: <protocol> <host:addr>")
		os.Exit(1)
	}

	protocol := os.Args[1]
	addr := os.Args[2]

	if protocol != "tcp" && protocol != "udp" {
		fmt.Println("Wrong protocol Choose from thoses two protocols: tcp | udp ")
		os.Exit(1)
	}

	if protocol == "tcp" {
		startTcpServer(addr)
	} else {
		startUdpServer(addr)
	}

}

func startUdpServer(addr string) {
	// Resolve a udp addr
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
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
	fmt.Printf("%s server listening on addr %s \n", "udp", addr)

	buffer := make([]byte, 1024)
	for {
		n, addr, err := connection.ReadFromUDP(buffer)
		if err != nil {
			fmt.Println("falied to read data, err: ", err)
			return
		}

		msg := string(buffer[:n])
		fmt.Println("request: ", msg)

		_, err = connection.WriteToUDP(buffer, addr)
		if err != nil {
			fmt.Println("failed to write date to client,err: ", err)
			return
		}
		fmt.Println("response:", msg)

	}

}

func handleTcpConnection(conn net.Conn) {
	defer conn.Close()

	msg, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		fmt.Println("falied to read data, err:", err)
		return
	}

	fmt.Printf("request: %s\n", msg)

	_, err = conn.Write([]byte(msg))
	if err != nil {
		fmt.Println("failed to write date to client,err: ", err)
		return
	}
	fmt.Println("response:", msg)
}

func startTcpServer(addr string) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("failed to create listener, err:", err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("%s server listening on addr %s \n", "tcp", addr)
	for {
		connection, err := listener.Accept()
		if err != nil {
			fmt.Println("failed to accept connection, err:", err)
			continue
		}

		go handleTcpConnection(connection)
	}

}
