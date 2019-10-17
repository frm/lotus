package peermgr

import (
	"context"
	"sync"
	"time"

	host "github.com/libp2p/go-libp2p-core/host"
	net "github.com/libp2p/go-libp2p-core/network"
	peer "github.com/libp2p/go-libp2p-core/peer"
	dht "github.com/libp2p/go-libp2p-kad-dht"

	logging "github.com/ipfs/go-log"
)

var log = logging.Logger("peermgr")

const (
	MaxFilPeers = 32
	MinFilPeers = 8
)

type PeerMgr struct {
	bootstrappers []peer.AddrInfo

	// peerLeads is a set of peers we hear about through the network
	// and who may be good peers to connect to for expanding our peer set
	peerLeads map[peer.ID]time.Time

	peersLk sync.Mutex
	peers   map[peer.ID]struct{}

	maxFilPeers int
	minFilPeers int

	expanding bool

	h   host.Host
	dht *dht.IpfsDHT

	notifee *net.NotifyBundle
}

func NewPeerMgr(h host.Host, dht *dht.IpfsDHT) *PeerMgr {
	pm := &PeerMgr{
		peers:       make(map[peer.ID]struct{}),
		maxFilPeers: MaxFilPeers,
		minFilPeers: MinFilPeers,
		h:           h,
	}

	pm.notifee = &net.NotifyBundle{
		DisconnectedF: func(_ net.Network, c net.Conn) {
			pm.Disconnect(c.RemotePeer())
		},
	}

	h.Network().Notify(pm.notifee)

	return pm
}

func (pmgr *PeerMgr) AddFilecoinPeer(p peer.ID) {
	pmgr.peersLk.Lock()
	defer pmgr.peersLk.Unlock()
	pmgr.peers[p] = struct{}{}
}

func (pmgr *PeerMgr) Disconnect(p peer.ID) {
	if pmgr.h.Network().Connectedness(p) == net.NotConnected {
		pmgr.peersLk.Lock()
		defer pmgr.peersLk.Unlock()
		delete(pmgr.peers, p)
	}
}

func (pmgr *PeerMgr) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second * 5)
	for {
		select {
		case <-tick.C:
			pcount := pmgr.getPeerCount()
			if pcount < pmgr.minFilPeers {
				pmgr.expandPeers()
			} else if pcount > pmgr.maxFilPeers {
				log.Infof("peer count about threshold: %d > %d", pcount, pmgr.maxFilPeers)
			}
		}
	}
}

func (pmgr *PeerMgr) getPeerCount() int {
	pmgr.peersLk.Lock()
	defer pmgr.peersLk.Unlock()
	return len(pmgr.peers)
}

func (pmgr *PeerMgr) expandPeers() {
	if pmgr.expanding {
		return
	}
	pmgr.expanding = true
	go func() {
		defer func() {
			pmgr.expanding = false
		}()
		ctx, cancel := context.WithTimeout(context.TODO(), time.Second*30)
		defer cancel()

		pmgr.doExpand(ctx)
	}()
}

func (pmgr *PeerMgr) doExpand(ctx context.Context) {
	pcount := pmgr.getPeerCount()
	if pcount == 0 {
		if len(pmgr.bootstrappers) == 0 {
			log.Warn("no peers connected, and no bootstrappers configured")
			return
		}

		log.Info("connecting to bootstrap peers")
		for _, bsp := range pmgr.bootstrappers {
			if err := pmgr.h.Connect(ctx, bsp); err != nil {
				log.Warnf("failed to connect to bootstrap peer: %s", err)
			}
		}
		return
	}

	// if we already have some peers and need more, the dht is really good at connecting to most peers. Use that for now until something better comes along.
	if err := pmgr.dht.Bootstrap(ctx); err != nil {
		log.Warnf("dht bootstrapping failed: %s", err)
	}
}
