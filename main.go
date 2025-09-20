package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Missing args: <protocol> <port>")
		os.Exit(1)
	}

	protocol := os.Args[1]
	addr := ":" + os.Args[2]

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
	var client *net.UDPAddr

	for {
		n, raddr, err := connection.ReadFromUDP(buffer)
		if err != nil {
			fmt.Println("falied to read data, err: ", err)
			continue
		}

		msg := strings.TrimSpace(string(buffer[:n]))
		fmt.Println("request: ", msg)

		if strings.HasPrefix(msg, "register") && client == nil {
			client = raddr
			ack := "ack register\n"
			_, err = connection.WriteToUDP([]byte(ack), client)
			if err != nil {
				fmt.Println("failed to write date to client,err: ", err)
				return
			}
			fmt.Println("registered client:", client.String())

			go func(c *net.UDPAddr) {
				ticker := time.NewTicker(10 * time.Second)
				defer ticker.Stop()

				// send some quick initial packets
				for i := 0; i < 5; i++ {
					test := fmt.Sprintf("server-initial %d -> %s\n", i, c.String())
					connection.WriteToUDP([]byte(test), c)
					time.Sleep(200 * time.Millisecond)
				}

				for range ticker.C {
					keep := fmt.Sprintf("server-keepalive -> %s time=%d\n", c.String(), time.Now().Unix())
					_, err := connection.WriteToUDP([]byte(keep), c)
					if err != nil {
						fmt.Println("error writing keepalive:", err)
						return
					}
				}
			}(client)
		} else {

			_, err = connection.WriteToUDP(buffer, raddr)
			if err != nil {
				fmt.Println("failed to write date to client,err: ", err)
				return
			}
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
