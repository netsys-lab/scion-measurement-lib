package measured_appnet

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/netsec-ethz/scion-apps/pkg/appnet"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/sciond"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/snet/addrutil"
	"github.com/scionproto/scion/go/lib/sock/reliable"
)

type Network struct {
	snet.Network
	IA            addr.IA
	PathQuerier   snet.PathQuerier
	hostInLocalAS net.IP
}

const (
	initTimeout = 1 * time.Second
)

var defNetwork Network
var initOnce sync.Once

func DefNetwork() *Network {
	initOnce.Do(mustInitDefNetwork)
	return &defNetwork
}

func Dial(address string) (*snet.Conn, error) {
	raddr, err := appnet.ResolveUDPAddr(address)
	if err != nil {
		return nil, err
	}
	return DialAddr(raddr)
}

func DialAddr(raddr *snet.UDPAddr) (*snet.Conn, error) {
	if raddr.Path.IsEmpty() {
		err := appnet.SetDefaultPath(raddr)
		if err != nil {
			return nil, err
		}
	}
	localIP, err := resolveLocal(raddr)
	if err != nil {
		return nil, err
	}
	laddr := &net.UDPAddr{IP: localIP}
	return DefNetwork().Dial(context.Background(), "udp", laddr, raddr, addr.SvcNone)
}

func Listen(listen *net.UDPAddr) (*snet.Conn, error) {
	if listen == nil {
		listen = &net.UDPAddr{}
	}
	if listen.IP == nil || listen.IP.IsUnspecified() {
		localIP, err := defaultLocalIP()
		if err != nil {
			return nil, err
		}
		listen = &net.UDPAddr{IP: localIP, Port: listen.Port, Zone: listen.Zone}
	}
	defNetwork := DefNetwork()
	integrationEnv, _ := os.LookupEnv("SCION_GO_INTEGRATION")
	if integrationEnv == "1" || integrationEnv == "true" || integrationEnv == "TRUE" {
		fmt.Printf("Listening ia=:%v\n", defNetwork.IA)
	}
	return defNetwork.Listen(context.Background(), "udp", listen, addr.SvcNone)
}

func ListenPort(port uint16) (*snet.Conn, error) {
	return Listen(&net.UDPAddr{Port: int(port)})
}

func resolveLocal(raddr *snet.UDPAddr) (net.IP, error) {
	if raddr.NextHop != nil {
		nextHop := raddr.NextHop.IP
		return addrutil.ResolveLocal(nextHop)
	}
	return defaultLocalIP()
}

func defaultLocalIP() (net.IP, error) {
	return addrutil.ResolveLocal(DefNetwork().hostInLocalAS)
}

func mustInitDefNetwork() {
	err := initDefNetwork()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing SCION network: %v\n", err)
		os.Exit(1)
	}
}

func initDefNetwork() error {
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	dispatcher, err := findDispatcher()
	if err != nil {
		return err
	}
	sciondConn, err := findSciond(ctx)
	if err != nil {
		return err
	}
	localIA, err := sciondConn.LocalIA(ctx)
	if err != nil {
		return err
	}
	hostInLocalAS, err := findAnyHostInLocalAS(ctx, sciondConn)
	if err != nil {
		return err
	}
	pathQuerier := sciond.Querier{Connector: sciondConn, IA: localIA}
	n := NewNetwork(
		localIA,
		dispatcher,
		sciond.RevHandler{Connector: sciondConn},
	)
	defNetwork = Network{Network: n, IA: localIA, PathQuerier: pathQuerier, hostInLocalAS: hostInLocalAS}
	return nil
}

func findSciond(ctx context.Context) (sciond.Connector, error) {
	address, ok := os.LookupEnv("SCION_DAEMON_ADDRESS")
	if !ok {
		address = sciond.DefaultAPIAddress
	}
	sciondConn, err := sciond.NewService(address).Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to SCIOND at %s (override with SCION_DAEMON_ADDRESS): %w", address, err)
	}
	return sciondConn, nil
}

func findDispatcher() (reliable.Dispatcher, error) {
	path, err := findDispatcherSocket()
	if err != nil {
		return nil, err
	}
	dispatcher := reliable.NewDispatcher(path)
	return dispatcher, nil
}

func findDispatcherSocket() (string, error) {
	path, ok := os.LookupEnv("SCION_DISPATCHER_SOCKET")
	if !ok {
		path = reliable.DefaultDispPath
	}
	err := statSocket(path)
	if err != nil {
		return "", fmt.Errorf("error looking for SCION dispatcher socket at %s (override with SCION_DISPATCHER_SOCKET): %w", path, err)
	}
	return path, nil
}

func statSocket(path string) error {
	fileinfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !isSocket(fileinfo.Mode()) {
		return fmt.Errorf("%s is not a socket (mode: %s)", path, fileinfo.Mode())
	}
	return nil
}

func isSocket(mode os.FileMode) bool {
	return mode&os.ModeSocket != 0
}

// findAnyHostInLocalAS returns the IP address of some (infrastructure) host in the local AS.
func findAnyHostInLocalAS(ctx context.Context, sciondConn sciond.Connector) (net.IP, error) {
	addr, err := sciond.TopoQuerier{Connector: sciondConn}.UnderlayAnycast(ctx, addr.SvcCS)
	if err != nil {
		return nil, err
	}
	return addr.IP, nil
}
