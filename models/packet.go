package models

import "net"

type IncomingPacket struct {
	OpCode   byte
	ID       uint32
	ClientID uint16
	Payload  []byte
	Addr     *net.UDPAddr
}

type OutgoingPacket struct {
	Payload  []byte
	Addr     *net.UDPAddr
	PacketID uint32
	ClientID uint16
	OpCode   byte
}
type Packet struct {
	Payload  []byte
	Addr     *net.UDPAddr
	ID       uint32
	ClientID uint16
	OpCode   byte
	Done     chan bool
	Length   uint16
}

type RawPacket struct {
	Data []byte
	Addr *net.UDPAddr
}
