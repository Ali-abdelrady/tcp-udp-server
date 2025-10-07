package workers

import (
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
	clients map[uint16]*net.UDPAddr
	ops     chan clientOp
}

func NewClientManager() *ClientManager {
	cm := &ClientManager{
		clients: make(map[uint16]*net.UDPAddr),
		ops:     make(chan clientOp),
	}
	go cm.run()
	return cm
}

func (cm *ClientManager) run() {
	for op := range cm.ops {
		switch op.action {
		case "add":
			cm.clients[op.clientID] = op.addr
		case "get":
			if addr, ok := cm.clients[op.clientID]; ok {
				op.reply <- addr
			} else {
				op.reply <- nil
			}
		case "list":
			// return a copy so callers can't mutate our map
			clone := make(map[uint16]*net.UDPAddr, len(cm.clients))
			for id, addr := range cm.clients {
				clone[id] = addr
			}
			op.reply <- clone
		case "remove":
			delete(cm.clients, op.clientID)
		}
	}
}

func (cm *ClientManager) AddClient(clientID uint16, addr *net.UDPAddr) {
	cm.ops <- clientOp{action: "add", addr: addr, clientID: clientID}
}

func (cm *ClientManager) GetClient(clientID uint16) *net.UDPAddr {
	reply := make(chan interface{})
	cm.ops <- clientOp{action: "get", clientID: clientID, reply: reply}
	if addr, ok := (<-reply).(*net.UDPAddr); ok {
		return addr
	}
	return nil
}

func (cm *ClientManager) ListClients() map[uint16]*net.UDPAddr {
	reply := make(chan interface{})
	cm.ops <- clientOp{action: "list", reply: reply}
	return (<-reply).(map[uint16]*net.UDPAddr)
}

func (cm *ClientManager) RemoveClient(clientID uint16) {
	cm.ops <- clientOp{action: "remove", clientID: clientID}
}
