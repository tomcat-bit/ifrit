package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	log "github.com/inconshreveable/log15"
	"github.com/joonnna/ifrit/core/discovery"
	"github.com/joonnna/ifrit/protobuf"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/net/context"
	"google.golang.org/grpc/credentials"
	grpcPeer "google.golang.org/grpc/peer"
)

type HandlerTestSuite struct {
	suite.Suite
	n *Node

	priv    *ecdsa.PrivateKey
	privMap map[string]*ecdsa.PrivateKey
}

func TestHandlerTestSuite(t *testing.T) {
	r := log.Root()

	r.SetHandler(log.CallerFileHandler(log.StreamHandler(os.Stdout, log.TerminalFormat())))
	//r.SetHandler(log.DiscardHandler())

	suite.Run(t, new(HandlerTestSuite))
}

func (suite *HandlerTestSuite) SetupTest() {
	priv, err := genKeys()
	require.NoError(suite.T(), err, "Failed to generate keys")

	numTestPeers := 100

	ownCert := genCert(priv, 10)

	n, err := NewNode(&commStub{}, &pingStub{}, &cmStub{cert: ownCert}, &cryptoStub{priv: priv})
	require.NoError(suite.T(), err, "Failed to create node.")

	suite.n = n

	suite.privMap = make(map[string]*ecdsa.PrivateKey)

	for i := 0; i < numTestPeers; i++ {
		p, priv, err := addPeer(suite.n)
		require.NoError(suite.T(), err, "Could not add peer.")
		suite.privMap[p.Id] = priv
	}

	suite.priv = priv
}

func (suite *HandlerTestSuite) TestSpread() {
	var nonExistingNeighbours []string
	var ringNum uint32 = 1

	node := suite.n

	testPeers := nonNeighbouringPeers(node, 5)

	for i, p := range testPeers {
		require.False(suite.T(), node.view.ShouldBeNeighbour(p.Id),
			"Peer should not be neighbour.")

		if i != 0 {
			require.False(suite.T(), node.view.IsAlive(p.Id), "Peer should be dead.")
		} else {
			require.True(suite.T(), node.view.IsAlive(p.Id), "Peer should be alive.")
		}
	}

	notNeighbour := testPeers[0]

	accusedNonNeighbour := testPeers[1]
	acc := discovery.NewAccusation(1, accusedNonNeighbour.Id, node.self.Id, 1,
		suite.priv)
	accusedNonNeighbour.AddTestAccusation(acc)

	rebuttalPeer := testPeers[2]
	acc2 := discovery.NewAccusation(1, rebuttalPeer.Id, node.self.Id, 1, suite.priv)
	rebuttalPeer.AddTestAccusation(acc2)

	nonExistingPeer := testPeers[3]
	node.view.RemoveTestFull(nonExistingPeer.Id)

	invalidNonExisting := testPeers[4]
	node.view.RemoveTestFull(invalidNonExisting.Id)

	succ, _ := node.view.MyRingNeighbours(ringNum)

	tests := []struct {
		ctx  context.Context
		args *proto.State

		peer *discovery.Peer

		specificErr error
		err         bool

		isAccused bool
		exists    bool
		live      bool

		certs []string
		notes []string
		accs  []string

		findNeighbours bool
	}{
		{
			ctx:    noCertPeerContext(succ),
			err:    true,
			exists: true,
			live:   true,
			peer:   succ,
		},

		{
			ctx:    invalidCertPeerContext(succ),
			err:    true,
			exists: true,
			live:   true,
			peer:   succ,
		},

		{
			ctx:    peerContext(succ),
			err:    false,
			exists: true,
			live:   true,
			peer:   succ,
		},

		{
			ctx:       peerContext(accusedNonNeighbour),
			err:       false,
			isAccused: true,
			accs:      []string{accusedNonNeighbour.Id},
			args: &proto.State{
				OwnNote: accusedNonNeighbour.Note().ToPbMsg(),
			},
			exists: true,
			live:   false,
			peer:   accusedNonNeighbour,
		},

		{
			ctx:       peerContext(rebuttalPeer),
			err:       false,
			isAccused: false,
			args: &proto.State{
				OwnNote: discovery.NewNote(rebuttalPeer.Id, 2, math.MaxUint32,
					suite.privMap[rebuttalPeer.Id]),
			},
			exists: true,
			live:   true,
			peer:   rebuttalPeer,
		},

		{
			ctx:         peerContext(notNeighbour),
			err:         false,
			specificErr: errNotMyNeighbour,
			exists:      true,
			live:        true,
			peer:        notNeighbour,
		},

		{
			ctx:    invalidCertPeerContext(invalidNonExisting),
			err:    true,
			exists: false,
			live:   false,
			peer:   invalidNonExisting,
		},

		{
			ctx:    peerContext(nonExistingPeer),
			err:    false,
			certs:  nonExistingNeighbours,
			notes:  nonExistingNeighbours,
			exists: true,
			live:   true,
			peer:   nonExistingPeer,
			args: &proto.State{
				OwnNote: nonExistingPeer.Note().ToPbMsg(),
			},
			findNeighbours: true,
		},
	}

	for i, t := range tests {
		reply, err := node.Spread(t.ctx, t.args)

		if !t.err {
			if t.specificErr != nil {
				require.EqualErrorf(suite.T(), err, t.specificErr.Error(),
					"Returned wrong  error in test %d.", i)
			}
		} else {
			require.Errorf(suite.T(), err, "Should return error in test %d.", i)
		}

		if !t.err && t.specificErr == nil {
			require.NotNil(suite.T(), reply, "Invalid response for test %d.", i)
		} else {
			require.Nil(suite.T(), reply, "Invalid response for test %d.", i)
		}

		require.Equalf(suite.T(), t.isAccused, t.peer.IsAccused(),
			"Invalid accused state for test %d.", i)
		require.Equalf(suite.T(), t.exists, node.view.Exists(t.peer.Id),
			"Invalid exists state for test %d.", i)
		require.Equalf(suite.T(), t.live, node.view.IsAlive(t.peer.Id),
			"Invalid live state for test %d.", i)

		var certs []string
		for _, c := range reply.GetCertificates() {
			cert, err := x509.ParseCertificate(c.GetRaw())
			require.NoError(suite.T(), err, "Failed to parse certificate.")

			certs = append(certs, string(cert.SubjectKeyId))
		}

		var notes []string
		for _, n := range reply.GetNotes() {
			notes = append(notes, string(n.GetId()))
		}

		var accs []string
		for _, a := range reply.GetAccusations() {
			accs = append(accs, string(a.GetAccused()))
		}

		if !t.findNeighbours {
			require.ElementsMatchf(suite.T(), t.certs, certs,
				"Invalid certificate output for test %d.", i)

			require.ElementsMatchf(suite.T(), t.notes, notes,
				"Invalid note output for test %d.", i)

			require.ElementsMatchf(suite.T(), t.accs, accs,
				"Invalid accusation output for test %d.", i)
		} else {
			var neighbours []string
			for _, p := range node.view.FindNeighbours(nonExistingPeer.Id) {
				neighbours = append(neighbours, p.Id)
			}

			neighbours = append(neighbours, node.self.Id)

			require.ElementsMatchf(suite.T(), neighbours, certs,
				"Invalid certificate output for test %d.", i)

			require.ElementsMatchf(suite.T(), neighbours, notes,
				"Invalid note output for test %d.", i)
		}
	}
}

func (suite *HandlerTestSuite) TestMessenger() {

}

func (suite *HandlerTestSuite) TestMergeViews() {
	node := suite.n

	full := node.view.Full()

	p1 := full[0]
	p2 := full[1]
	p3 := full[2]

	p1.ClearNote()

	state := make(map[string]uint64)

	for _, p := range full {
		if p.Id == p1.Id || p.Id == p2.Id {
			continue
		}

		state[p.Id] = 1
	}

	state[p3.Id] = 2
	state[node.self.Id] = 1

	acc1 := discovery.NewAccusation(2, p3.Id, p2.Id, 1, suite.privMap[p2.Id])
	p3.AddTestAccusation(acc1)

	acc2 := discovery.NewAccusation(2, p3.Id, p2.Id, 2, suite.privMap[p2.Id])
	p3.AddTestAccusation(acc2)

	acc3 := discovery.NewAccusation(2, p3.Id, p2.Id, 3, suite.privMap[p2.Id])
	p3.AddTestAccusation(acc3)

	require.True(suite.T(), node.view.Exists(p1.Id), "Peer 1 should exist in full view.")
	require.True(suite.T(), node.view.Exists(p2.Id), "Peer 2 should exist in full view.")
	require.True(suite.T(), node.view.Exists(p3.Id), "Peer 3 should exist in full view.")
	require.Equal(suite.T(), len(p3.AllAccusations()), 3,
		"Should have 3 accusations on peer 3.")

	tests := []struct {
		in    map[string]uint64
		certs []string
		notes []string
		accs  []string
	}{
		{
			in:    state,
			certs: []string{p1.Id, p2.Id},
			notes: []string{p2.Id, p3.Id},
			accs:  []string{p3.Id, p3.Id, p3.Id},
		},
	}

	for i, t := range tests {
		reply := &proto.StateResponse{}
		node.mergeViews(t.in, reply)

		var certs []string
		for _, c := range reply.GetCertificates() {
			cert, err := x509.ParseCertificate(c.GetRaw())
			require.NoError(suite.T(), err, "Failed to parse certificate.")

			certs = append(certs, string(cert.SubjectKeyId))
		}

		var notes []string
		for _, n := range reply.GetNotes() {
			notes = append(notes, string(n.GetId()))
		}

		var accs []string
		for _, a := range reply.GetAccusations() {
			accs = append(accs, string(a.GetAccused()))
		}

		require.ElementsMatchf(suite.T(), t.certs, certs,
			"Invalid certificate output for test %d.", i)

		require.ElementsMatchf(suite.T(), t.notes, notes,
			"Invalid note output for test %d.", i)

		require.ElementsMatchf(suite.T(), t.accs, accs,
			"Invalid accusation output for test %d.", i)
	}

}

func (suite *HandlerTestSuite) TestMergeNotes() {

}

func (suite *HandlerTestSuite) TestMergeAccusations() {

}

func (suite *HandlerTestSuite) TestMergeCertificates() {

}

func (suite *HandlerTestSuite) TestEvalAccusation() {
	var ringNum uint32 = 1
	var randomPeer *discovery.Peer

	node := suite.n

	succ, prev := node.view.MyRingNeighbours(ringNum)

	for _, p := range node.view.Live() {
		if p.Id != succ.Id && p.Id != prev.Id {
			randomPeer = p
			break
		}
	}

	randomPeer.ClearNote()

	selfId := node.self.Id

	tests := []struct {
		acc     *proto.Accusation
		accuser *discovery.Peer
		accused *discovery.Peer
		out     error

		timer     bool
		rebuttal  bool
		prevNote  *discovery.Note
		isAccused bool
	}{
		{
			acc:       discovery.NewAccusation(1, selfId, succ.Id, 1, suite.privMap[succ.Id]),
			accuser:   succ,
			accused:   node.self,
			out:       errInvalidAccuser,
			timer:     false,
			rebuttal:  false,
			isAccused: false,
		},

		{
			acc:       discovery.NewUnsignedAccusation(1, selfId, prev.Id, 1),
			accuser:   prev,
			accused:   node.self,
			out:       errInvalidSignature,
			timer:     false,
			rebuttal:  false,
			isAccused: false,
		},

		{
			acc:       discovery.NewAccusation(2, selfId, prev.Id, 1, suite.privMap[prev.Id]),
			accuser:   prev,
			accused:   node.self,
			out:       errInvalidSelfAccusation,
			timer:     false,
			rebuttal:  false,
			isAccused: false,
		},

		{
			acc: discovery.NewAccusation(1, selfId, prev.Id, 1,
				suite.privMap[prev.Id]),
			accuser:   prev,
			accused:   node.self,
			out:       nil,
			timer:     false,
			rebuttal:  true,
			prevNote:  node.self.Note(),
			isAccused: false,
		},

		{
			acc: discovery.NewAccusation(1, succ.Id, prev.Id, 1,
				suite.privMap[prev.Id]),
			accuser:   prev,
			accused:   succ,
			out:       errInvalidAccuser,
			timer:     false,
			rebuttal:  false,
			isAccused: false,
		},

		{
			acc:       discovery.NewAccusation(2, succ.Id, selfId, 1, suite.priv),
			accuser:   node.self,
			accused:   succ,
			out:       errInvalidEpoch,
			timer:     false,
			rebuttal:  false,
			isAccused: false,
		},

		{
			acc:       discovery.NewUnsignedAccusation(1, succ.Id, selfId, 1),
			accuser:   node.self,
			accused:   succ,
			out:       errInvalidSignature,
			timer:     false,
			rebuttal:  false,
			isAccused: false,
		},

		{
			acc:       discovery.NewAccusation(1, succ.Id, selfId, 1, suite.priv),
			accuser:   node.self,
			accused:   succ,
			out:       nil,
			timer:     true,
			rebuttal:  false,
			isAccused: true,
		},

		{
			acc:       discovery.NewAccusation(1, succ.Id, selfId, 1, suite.priv),
			accuser:   node.self,
			accused:   succ,
			out:       errAccAlreadyExists,
			timer:     true,
			rebuttal:  false,
			isAccused: true,
		},
	}

	for i, t := range tests {
		require.Equalf(suite.T(), t.out, node.evalAccusation(t.acc, t.accuser, t.accused),
			"Invalid output for test %d.", i)

		if t.accused != nil {
			require.Equalf(suite.T(), t.timer, node.view.HasTimer(t.accused.Id),
				"Invalid timer state for test %d.", i)

			require.Equalf(suite.T(), t.isAccused, t.accused.IsAccused(),
				"Invalid accusation state for test %d.", i)
		}

		if t.rebuttal {
			require.NotEqualf(suite.T(), t.prevNote, t.accused.Note(),
				"Note note changed after rebuttal for test %d.", i)
		}

		node.view.DeleteTimeout(succ.Id)
		node.view.DeleteTimeout(prev.Id)
		node.view.DeleteTimeout(node.self.Id)
		node.view.DeleteTimeout(randomPeer.Id)
	}

}

func (suite *HandlerTestSuite) TestEvalNote() {
	node := suite.n

	mask := uint32(math.MaxUint32)

	live := node.view.Live()
	peer := live[0]
	peer2 := live[1]
	peer3 := live[2]

	peer2.ClearNote()

	acc := discovery.NewAccusation(2, peer3.Id, peer2.Id, 1, suite.privMap[peer2.Id])
	peer3.AddTestAccusation(acc)

	err := node.view.StartTimer(peer3, peer3.Note(), peer2)
	require.NoError(suite.T(), err, "Failed to start timer.")

	tests := []struct {
		note *proto.Note
		out  error

		noteHolder  *discovery.Peer
		isAccused   bool
		isAlive     bool
		timer       bool
		replaceNote bool
		prevNote    *discovery.Note
	}{
		{
			note:        discovery.NewNote("Non-existing Id", 1, mask, suite.privMap[peer.Id]),
			out:         errNoPeer,
			noteHolder:  peer,
			isAccused:   false,
			isAlive:     false,
			timer:       false,
			replaceNote: false,
		},

		{
			note:        discovery.NewNote(peer.Id, 1, mask, suite.privMap[peer.Id]),
			out:         errOldNote,
			noteHolder:  peer,
			isAccused:   false,
			isAlive:     false,
			timer:       false,
			replaceNote: false,
		},

		{
			note:        discovery.NewNote(peer.Id, 2, 0, suite.privMap[peer.Id]),
			out:         errInvalidMask,
			noteHolder:  peer,
			isAccused:   false,
			isAlive:     false,
			timer:       false,
			replaceNote: false,
		},

		{
			note:        discovery.NewUnsignedNote(peer.Id, 2, mask),
			out:         errInvalidSignature,
			noteHolder:  peer,
			isAccused:   false,
			isAlive:     false,
			timer:       false,
			replaceNote: false,
		},

		{
			note:        discovery.NewNote(peer.Id, 2, mask, suite.privMap[peer.Id]),
			out:         nil,
			noteHolder:  peer,
			isAccused:   false,
			isAlive:     true,
			timer:       false,
			replaceNote: true,
			prevNote:    peer.Note(),
		},

		{
			note:        discovery.NewNote(peer2.Id, 1, mask, suite.privMap[peer2.Id]),
			out:         nil,
			noteHolder:  peer2,
			isAccused:   false,
			isAlive:     true,
			timer:       false,
			replaceNote: true,
			prevNote:    peer2.Note(),
		},

		{
			note:        discovery.NewUnsignedNote(peer3.Id, 2, mask),
			out:         errInvalidSignature,
			noteHolder:  peer3,
			isAccused:   true,
			isAlive:     false,
			timer:       true,
			replaceNote: false,
		},

		{
			note:        discovery.NewNote(peer3.Id, 2, mask, suite.privMap[peer3.Id]),
			out:         nil,
			noteHolder:  peer3,
			isAccused:   true,
			isAlive:     false,
			timer:       true,
			replaceNote: false,
		},

		{
			note:        discovery.NewNote(peer3.Id, 3, mask, suite.privMap[peer3.Id]),
			out:         nil,
			noteHolder:  peer3,
			isAccused:   false,
			isAlive:     true,
			timer:       false,
			replaceNote: true,
			prevNote:    peer3.Note(),
		},
	}

	for i, t := range tests {
		node.view.RemoveLive(peer.Id)
		node.view.RemoveLive(peer2.Id)
		node.view.RemoveLive(peer3.Id)

		require.Equalf(suite.T(), t.out, node.evalNote(t.note),
			"Invalid output for test %d.", i)
		require.Equalf(suite.T(), t.isAccused, t.noteHolder.IsAccused(),
			"Invalid accusation state for test %d.", i)
		require.Equalf(suite.T(), t.isAlive, node.view.IsAlive(t.noteHolder.Id),
			"Invalid live state for test %d.", i)
		require.Equalf(suite.T(), t.timer, node.view.HasTimer(t.noteHolder.Id),
			"Invalid timer state for test %d.", i)

		if t.replaceNote {
			require.NotEqualf(suite.T(), t.prevNote, t.noteHolder.Note(),
				"Invalid timer state for test %d.", i)
		}

	}
}

func (suite *HandlerTestSuite) TestEvalCertificate() {
	node := suite.n

	rings := node.view.NumRings()
	live := node.view.Live()
	peer := live[0]

	tests := []struct {
		cert   *x509.Certificate
		out    error
		exists bool

		nonNilError bool
	}{
		{
			cert:        nil,
			out:         errNilCert,
			exists:      false,
			nonNilError: false,
		},

		{
			cert:        node.cm.Certificate(),
			out:         errSelfCert,
			exists:      false,
			nonNilError: false,
		},

		{
			cert:        genInvalidIdCert(suite.privMap[peer.Id], rings),
			out:         errInvalidId,
			exists:      false,
			nonNilError: false,
		},

		{
			cert:        genInvalidSignatureCert(suite.privMap[peer.Id], rings),
			out:         errSelfCert,
			exists:      false,
			nonNilError: true,
		},

		{
			cert:        genCert(suite.privMap[peer.Id], rings),
			out:         nil,
			exists:      true,
			nonNilError: false,
		},

		{
			cert:        genCert(suite.privMap[peer.Id], rings),
			out:         nil,
			exists:      true,
			nonNilError: false,
		},
	}
	for i, t := range tests {
		if !t.nonNilError {
			require.Equalf(suite.T(), t.out, node.evalCertificate(t.cert),
				"Invalid output for test %d.", i)
		} else {
			require.Errorf(suite.T(), node.evalCertificate(t.cert),
				"Invalid output for test %d.", i)
		}

		if t.cert != nil {
			require.Equalf(suite.T(), t.exists, node.view.Exists(string(t.cert.SubjectKeyId)),
				"Invalid exists state for test %d.", i)
		}
	}
}

func (suite *HandlerTestSuite) TestValidateCtx() {
	node := suite.n

	live := node.view.Live()
	peer := live[0]

	validCert := genCert(suite.privMap[peer.Id], node.view.NumRings())

	noAuthInfo := &grpcPeer.Peer{
		AuthInfo: credentials.TLSInfo{},
	}

	validAuthInfo := &grpcPeer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{validCert},
			},
		},
	}

	tests := []struct {
		ctx context.Context
		out error

		cert *x509.Certificate
	}{
		{
			ctx:  context.Background(),
			out:  errNoPeerInCtx,
			cert: nil,
		},

		{
			ctx:  grpcPeer.NewContext(context.Background(), &grpcPeer.Peer{}),
			out:  errNoTLSInfo,
			cert: nil,
		},

		{
			ctx:  grpcPeer.NewContext(context.Background(), noAuthInfo),
			out:  errNoCert,
			cert: nil,
		},

		{
			ctx:  grpcPeer.NewContext(context.Background(), validAuthInfo),
			out:  nil,
			cert: validCert,
		},
	}
	for i, t := range tests {
		c, err := node.validateCtx(t.ctx)
		require.Equalf(suite.T(), t.out, err, "Invalid output for test %d.", i)
		require.Equalf(suite.T(), t.cert, c, "Invalid output for test %d.", i)
	}

}

func peerContext(p *discovery.Peer) context.Context {
	cert, err := x509.ParseCertificate(p.Certificate())
	if err != nil {
		panic(err)
	}

	authInfo := &grpcPeer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{cert},
			},
		},
	}

	return grpcPeer.NewContext(context.Background(), authInfo)
}

func noCertPeerContext(p *discovery.Peer) context.Context {
	authInfo := &grpcPeer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{},
		},
	}

	return grpcPeer.NewContext(context.Background(), authInfo)
}

func invalidCertPeerContext(p *discovery.Peer) context.Context {
	cert, err := x509.ParseCertificate(p.Certificate())
	if err != nil {
		panic(err)
	}

	cert.Signature = []byte("Invalid signature")

	authInfo := &grpcPeer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{cert},
			},
		},
	}

	return grpcPeer.NewContext(context.Background(), authInfo)
}

func nonNeighbouringPeers(n *Node, amount int) []*discovery.Peer {
	var fetched []*discovery.Peer

	for i := 0; i < amount; i++ {
		for _, p := range n.view.Full() {
			if !n.view.ShouldBeNeighbour(p.Id) {
				counter := 0
				for _, p2 := range fetched {
					if p.Id == p2.Id {
						break
					}
					counter++
				}

				if counter == len(fetched) {
					if i != 0 {
						n.view.RemoveLive(p.Id)
					}
					fetched = append(fetched, p)
					break
				}
			}
		}
	}

	invalid := checkPeerPlacement(n, fetched)
	if invalid != nil {
		for _, p := range invalid {
			n.view.AddLive(p)
		}

		return nonNeighbouringPeers(n, amount)
	}

	return fetched
}

func checkPeerPlacement(n *Node, peers []*discovery.Peer) []*discovery.Peer {
	var invalid []*discovery.Peer

	for _, p := range peers {
		if n.view.ShouldBeNeighbour(p.Id) {
			invalid = append(invalid, p)
		}

	}

	return invalid
}

func addPeer(node *Node) (*discovery.Peer, *ecdsa.PrivateKey, error) {
	privKey, err := genKeys()
	if err != nil {
		return nil, nil, err
	}

	c := genCert(privKey, node.view.NumRings())

	id := string(c.SubjectKeyId)

	err = node.view.AddFull(id, c)
	if err != nil {
		return nil, nil, err
	}

	p := node.view.Peer(id)

	node.view.AddLive(p)

	p.NewNote(privKey, 1)

	return p, privKey, nil
}

func genCert(priv *ecdsa.PrivateKey, rings uint32) *x509.Certificate {
	pk := pkix.Name{
		Locality: []string{"127.0.0.1:8000", "pingAddr", "httpAddr"},
	}

	c, err := selfSignedCert(priv, pk)
	if err != nil {
		panic(err)
	}

	return c
}

func genInvalidSignatureCert(priv *ecdsa.PrivateKey, rings uint32) *x509.Certificate {
	pk := pkix.Name{
		Locality: []string{"127.0.0.1:8000", "pingAddr", "httpAddr"},
	}

	c, err := selfSignedCert(priv, pk)
	if err != nil {
		panic(err)
	}

	c.Signature = []byte("Invalid signature")

	return c
}

func genInvalidIdCert(priv *ecdsa.PrivateKey, rings uint32) *x509.Certificate {
	pk := pkix.Name{
		Locality: []string{"127.0.0.1:8000", "pingAddr", "httpAddr"},
	}

	c, err := selfSignedCert(priv, pk)
	if err != nil {
		panic(err)
	}

	c.SubjectKeyId = []byte("Invalid id")

	return c
}

func genKeys() (*ecdsa.PrivateKey, error) {
	privKey, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		return nil, err
	}

	return privKey, nil
}

func selfSignedCert(priv *ecdsa.PrivateKey, pk pkix.Name) (*x509.Certificate, error) {
	ringBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(ringBytes[0:], uint32(32))

	ext := pkix.Extension{
		Id:       []int{2, 5, 13, 37},
		Critical: false,
		Value:    ringBytes,
	}

	serviceAddr := strings.Split(pk.Locality[0], ":")
	if len(serviceAddr) <= 0 {
		return nil, errors.New("No service addr in identity")
	}

	ip := net.ParseIP(serviceAddr[0])
	if ip == nil {
		return nil, errors.New("Could not parse ip address")
	}

	serial, err := genSerialNumber()
	if err != nil {
		return nil, err
	}

	// TODO generate ids and serial numbers differently
	newCert := &x509.Certificate{
		SerialNumber:          serial,
		SubjectKeyId:          genId(),
		Subject:               pk,
		BasicConstraintsValid: true,
		NotBefore:             time.Now().AddDate(-10, 0, 0),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		ExtraExtensions:       []pkix.Extension{ext},
		PublicKey:             priv.PublicKey,
		IPAddresses:           []net.IP{ip},
		IsCA:                  true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment |
			x509.KeyUsageCertSign,
	}

	signedCert, err := x509.CreateCertificate(rand.Reader, newCert,
		newCert, priv.Public(), priv)
	if err != nil {
		return nil, err
	}

	parsed, err := x509.ParseCertificate(signedCert)
	if err != nil {
		return nil, err
	}

	return parsed, nil
}

func genId() []byte {
	nonce := make([]byte, 32)
	rand.Read(nonce)
	return nonce

}

func genSerialNumber() (*big.Int, error) {
	sLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	s, err := rand.Int(rand.Reader, sLimit)
	if err != nil {
		return nil, err
	}

	return s, nil
}
