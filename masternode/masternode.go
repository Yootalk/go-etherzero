package masternode

import (
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/ethzero/go-ethzero/common"
	"github.com/ethzero/go-ethzero/contracts/masternode/contract"
	"github.com/ethzero/go-ethzero/crypto/sha3"
	"github.com/ethzero/go-ethzero/log"
	"github.com/ethzero/go-ethzero/p2p"
	"github.com/ethzero/go-ethzero/p2p/discover"
	"github.com/ethzero/go-ethzero/rlp"
	"math/big"
	"net"
	"sync"
)

const (
	MasternodeUnconnected = iota
	MasternodeConnected
	MasternodeDisconnected
)

var (
	errClosed            = errors.New("masternode set is closed")
	errAlreadyRegistered = errors.New("masternode is already registered")
	errNotRegistered     = errors.New("masternode is not registered")
)

type Masternode struct {
	ID                         string
	Node                       *discover.Node
	Account                    common.Address
	OriginBlock                uint64
	Height                     *big.Int
	State                      int
	CollateralMinConfBlockHash common.Hash
	ProtocolVersion            uint
}

func newMasternode(nodeId discover.NodeID, ip net.IP, port uint16, account common.Address, block uint64) *Masternode {
	id := GetMasternodeID(nodeId)
	n := discover.NewNode(nodeId, ip, 0, port)
	return &Masternode{
		ID:              id,
		Node:            n,
		Account:         account,
		OriginBlock:     block,
		State:           0,
		ProtocolVersion: 64,
	}
}

func (n *Masternode) String() string {
	return n.Node.String()
}

func (n *Masternode) CalculateScore(hash common.Hash) *big.Int {
	blockHash := rlpHash([]interface{}{
		hash,
		n.Account,
		n.CollateralMinConfBlockHash,
	})
	return blockHash.Big()
}

type MasternodeSet struct {
	nodes       map[string]*Masternode
	lock        sync.RWMutex
	closed      bool
	contract    *contract.Contract
	initialized bool
	SelfID      string
	PrivateKey  *ecdsa.PrivateKey
}

func NewMasternodeSet() *MasternodeSet {
	return &MasternodeSet{
		nodes: make(map[string]*Masternode),
	}
}

func (ns *MasternodeSet) Init(contract *contract.Contract, srvr *p2p.Server) {
	var (
		lastId [8]byte
		ctx    *MasternodeContext
	)
	lastId, err := contract.LastId(nil)
	if err != nil {
		return
	}
	for lastId != ([8]byte{}) {
		ctx, err = GetMasternodeContext(contract, lastId)
		if err != nil {
			log.Error("Init NodeSet", "error", err)
			break
		}
		ns.Register(ctx.Node)
		lastId = ctx.pre
	}
	ns.SelfID = GetMasternodeID(srvr.Self().ID)
	ns.contract = contract
	ns.PrivateKey = srvr.Config.PrivateKey
	ns.initialized = true
}

func (ns *MasternodeSet) Initialized() bool {
	return ns.initialized
}

// Register injects a new node into the working set, or returns an error if the
// node is already known.
func (ns *MasternodeSet) Register(n *Masternode) error {
	ns.lock.Lock()
	defer ns.lock.Unlock()

	if ns.closed {
		return errClosed
	}
	if _, ok := ns.nodes[n.ID]; ok {
		return errAlreadyRegistered
	}
	ns.nodes[n.ID] = n
	return nil
}

// Unregister removes a remote peer from the active set, disabling any further
// actions to/from that particular entity.
func (ns *MasternodeSet) Unregister(id string) error {
	ns.lock.Lock()
	defer ns.lock.Unlock()

	if _, ok := ns.nodes[id]; !ok {
		return errNotRegistered
	}
	delete(ns.nodes, id)
	return nil
}

// Peer retrieves the registered peer with the given id.
func (ns *MasternodeSet) Node(id string) *Masternode {
	ns.lock.RLock()
	defer ns.lock.RUnlock()

	return ns.nodes[id]
}

func (ns *MasternodeSet) SetState(id string, state int) bool {
	ns.lock.RLock()
	defer ns.lock.RUnlock()

	n := ns.nodes[id]
	if n == nil {
		return false
	}
	n.State = state
	return true
}

// Close disconnects all nodes.
// No new nodes can be registered after Close has returned.
func (ns *MasternodeSet) Close() {
	ns.lock.Lock()
	defer ns.lock.Unlock()

	//for _, p := range ns.nodes {
	//	p.Disconnect(p2p.DiscQuitting)
	//}
	ns.closed = true
}

func (ns *MasternodeSet) NodeJoin(id [8]byte) (*Masternode, error) {
	ctx, err := GetMasternodeContext(ns.contract, id)
	if err != nil {
		return &Masternode{}, err
	}
	err = ns.Register(ctx.Node)
	if err != nil {
		return &Masternode{}, err
	}
	return ctx.Node, nil
}

func (ns *MasternodeSet) NodeQuit(id [8]byte) {
	ns.Unregister(fmt.Sprintf("%x", id[:8]))
}

func (ns *MasternodeSet) Show() {
	for _, n := range ns.nodes {
		fmt.Println(n.String())
	}
}

func (ns *MasternodeSet) GetNodes() *map[string]*Masternode {
	return &ns.nodes
}

func (ns *MasternodeSet) Len() int {
	return len(ns.nodes)
}

func GetMasternodeID(ID discover.NodeID) string {
	return fmt.Sprintf("%x", ID[:8])
}

type MasternodeContext struct {
	Node *Masternode
	pre  [8]byte
	next [8]byte
}

func GetMasternodeContext(contract *contract.Contract, id [8]byte) (*MasternodeContext, error) {
	data, err := contract.ContractCaller.GetInfo(nil, id)
	if err != nil {
		return &MasternodeContext{}, err
	}
	// version := int(data.Misc[0])
	var ip net.IP = data.Misc[1:17]
	port := binary.BigEndian.Uint16(data.Misc[17:19])
	nodeId, _ := discover.BytesID(append(data.Id1[:], data.Id2[:]...))
	node := newMasternode(nodeId, ip, port, data.Account, data.BlockNumber.Uint64())

	return &MasternodeContext{
		Node: node,
		pre:  data.PreId,
		next: data.NextId,
	}, nil
}

func rlpHash(x interface{}) (h common.Hash) {
	hw := sha3.NewKeccak256()
	rlp.Encode(hw, x)
	hw.Sum(h[:0])
	return h
}
