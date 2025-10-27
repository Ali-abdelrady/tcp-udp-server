package models

import (
	"net"
	"time"
)

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
	Payload   []byte
	Addr      *net.UDPAddr
	ID        uint32
	ClientID  uint16
	OpCode    byte
	Done      chan bool
	Length    uint16
	Trackable bool
}

type RawPacket struct {
	Data []byte
	Addr *net.UDPAddr
}

type PendingPacket struct {
	Packet   Packet
	SendTime time.Time
	Retries  int
	AckChan  chan bool
}
