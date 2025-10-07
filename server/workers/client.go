package workers

import (
	"fmt"
	"net"
	"time"
)

type Client struct {
	ID       uint32
	Addr     *net.UDPAddr
	LastSeen time.Time
	IsOnline bool
}

type clientOp struct {
	action   string
	addr     *net.UDPAddr
	reply    chan interface{}
	clientID uint16
}

type ClientManager struct {
	clients map[uint16]Client
	ops     chan clientOp
}

func NewClientManager() *ClientManager {
	cm := &ClientManager{
		clients: make(map[uint16]Client),
		ops:     make(chan clientOp),
	}
	go cm.run()
	go cm.monitorClients()
	return cm
}

func (cm *ClientManager) run() {
	for op := range cm.ops {
		switch op.action {
		case "add":
			client := Client{ID: uint32(op.clientID), Addr: op.addr, LastSeen: time.Now(), IsOnline: true}
			cm.clients[op.clientID] = client

		case "ping":
			if client, ok := cm.clients[op.clientID]; ok {
				client.LastSeen = time.Now()
				client.IsOnline = true
				cm.clients[op.clientID] = client
			}

		case "get":
			if client, ok := cm.clients[op.clientID]; ok {
				op.reply <- client.Addr
			} else {
				op.reply <- nil
			}

		case "list":
			clone := make(map[uint16]Client, len(cm.clients))
			for id, clientObj := range cm.clients {
				clone[id] = clientObj
			}
			op.reply <- clone

		case "remove":
			delete(cm.clients, op.clientID)

		case "checkTimeouts":
			now := time.Now()
			for id, client := range cm.clients {
				if client.IsOnline && now.Sub(client.LastSeen) > 28*time.Second {
					client.IsOnline = false
					cm.clients[id] = client
					fmt.Printf("⚠ Client %d marked offline (no ping for 20s)\n", id)
				}
			}
		}
	}
}

func (cm *ClientManager) AddClient(clientID uint16, addr *net.UDPAddr) {
	cm.ops <- clientOp{action: "add", addr: addr, clientID: clientID}
}

func (cm *ClientManager) PingClient(clientID uint16) {
	cm.ops <- clientOp{action: "ping", clientID: clientID}
}

func (cm *ClientManager) GetClient(clientID uint16) *net.UDPAddr {
	reply := make(chan interface{})
	cm.ops <- clientOp{action: "get", clientID: clientID, reply: reply}
	if addr, ok := (<-reply).(*net.UDPAddr); ok {
		return addr
	}
	return nil
}

func (cm *ClientManager) ListClients() map[uint16]Client {
	reply := make(chan interface{})
	cm.ops <- clientOp{action: "list", reply: reply}
	return (<-reply).(map[uint16]Client)
}

func (cm *ClientManager) RemoveClient(clientID uint16) {
	cm.ops <- clientOp{action: "remove", clientID: clientID}
}

func (cm *ClientManager) monitorClients() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		cm.ops <- clientOp{action: "checkTimeouts"}
	}
}
