package rpc

import (
	"crypto/tls"
	"errors"
	"net"
	"os"
	"time"

	"github.com/joonnna/firechain/lib/netutils"
	"github.com/joonnna/firechain/lib/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

var (
	errInvalidInterface = errors.New("Provided interface is invalid")
)

type Server struct {
	rpcServer *grpc.Server

	listener net.Listener
}

func NewServer() *Server {
	//hostName := netutils.GetLocalIP()
	//hostName := "0.0.0.0"
	hostName, _ := os.Hostname()
	l, err := netutils.GetListener(hostName)
	if err != nil {
		panic(err)
	}

	return &Server{
		listener: l,
	}
}

func (s *Server) Init(config *tls.Config, n interface{}, maxConcurrent uint32) error {
	var serverOpts []grpc.ServerOption

	registerInterface, ok := n.(gossip.GossipServer)
	if !ok {
		return errInvalidInterface
	}

	keepAlive := keepalive.ServerParameters{
		MaxConnectionIdle: time.Minute * 20,
		Time:              time.Minute * 20,
	}

	creds := credentials.NewTLS(config)

	//comp := grpc.NewGZIPCompressor()
	//decomp := grpc.NewGZIPDecompressor()

	serverOpts = append(serverOpts, grpc.Creds(creds))
	//serverOpts = append(serverOpts, grpc.RPCCompressor(comp))
	//serverOpts = append(serverOpts, grpc.RPCDecompressor(decomp))
	serverOpts = append(serverOpts, grpc.KeepaliveParams(keepAlive))

	serverOpts = append(serverOpts, grpc.MaxConcurrentStreams(maxConcurrent))

	s.rpcServer = grpc.NewServer(serverOpts...)

	gossip.RegisterGossipServer(s.rpcServer, registerInterface)

	return nil
}

func (s *Server) Start() error {
	return s.rpcServer.Serve(s.listener)
}

func (s *Server) ShutDown() {
	s.rpcServer.Stop()
}

func (s *Server) HostInfo() string {
	/*
		port := strings.Split(s.listener.Addr().String(), ":")[1]
		host, _ := os.Hostname()

		addrs, err := net.LookupHost(host)
		if err != nil {
			return ""
		}

		return fmt.Sprintf("%s:%s", addrs[0], port)
	*/
	return s.listener.Addr().String()
}
