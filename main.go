package main

import (
	"fmt"
	"hole-punching-v2/server"
	"os"
)

func main() {
	args := os.Args
	if len(args) < 2 {
		fmt.Println("Missing <port>")
		os.Exit(1)
	}

	port := ":" + args[1]

	server := server.Udp{Port: port}

	server.StartServer()

}
