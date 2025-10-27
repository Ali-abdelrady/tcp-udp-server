package main

import (
	"fmt"
	"hole-punching-v2/server"
	"log"
	"os"
)

func main() {
	args := os.Args
	if len(args) < 2 {
		fmt.Println("Missing <port>")
		os.Exit(1)
	}

	port := ":" + args[1]

	f, err := os.OpenFile("server.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		log.Fatalf("error opening log file: %v", err)
	}
	defer f.Close()

	// Redirect standard logger output to the file
	log.SetOutput(f)

	// Optional: add timestamp + file info
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	// Example usage
	log.Println("Server started")

	server := server.NewUdp(port)
	server.StartServer()

}
