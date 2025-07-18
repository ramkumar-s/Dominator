package srpc

import (
	"bufio"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	libnet "github.com/Cloud-Foundations/Dominator/lib/net"
	"github.com/Cloud-Foundations/tricorder/go/tricorder"
	"github.com/Cloud-Foundations/tricorder/go/tricorder/units"
)

type endpointType struct {
	coderMaker coderMaker
	path       string
	tls        bool
}

var (
	attemptTransportUpgrade     = true // Changed by tests.
	clientMetricsDir            *tricorder.DirectorySpec
	clientMetricsMutex          sync.Mutex
	numInUseClientConnections   uint64
	numOpenCallConnections      uint64
	numOpenClientConnections    uint64
	setupClientExpirationMetric sync.Once
)

func init() {
	registerClientMetrics()
}

func registerClientMetrics() {
	var err error
	clientMetricsDir, err = tricorder.RegisterDirectory("srpc/client")
	if err != nil {
		panic(err)
	}
	err = clientMetricsDir.RegisterMetric("num-in-use-connections",
		&numInUseClientConnections, units.None,
		"number of client connections in use")
	if err != nil {
		panic(err)
	}
	err = clientMetricsDir.RegisterMetric("num-open-calls",
		&numOpenCallConnections, units.None, "number of open call connections")
	if err != nil {
		panic(err)
	}
	err = clientMetricsDir.RegisterMetric("num-open-connections",
		&numOpenClientConnections, units.None,
		"number of open client connections")
	if err != nil {
		panic(err)
	}
}

func dial(network, address string, dialer Dialer) (net.Conn, error) {
	hostPort := strings.SplitN(address, ":", 2)
	address = strings.SplitN(hostPort[0], "*", 2)[0] + ":" + hostPort[1]
	conn, err := dialer.Dial(network, address)
	if err != nil {
		if strings.Contains(err.Error(), ErrorConnectionRefused.Error()) {
			return nil, ErrorConnectionRefused
		}
		if strings.Contains(err.Error(), ErrorNoRouteToHost.Error()) {
			return nil, ErrorNoRouteToHost
		}
		return nil, err
	}
	if tcpConn, ok := conn.(libnet.TCPConn); ok {
		if err := tcpConn.SetKeepAlive(true); err != nil {
			conn.Close()
			return nil, err
		}
		err := tcpConn.SetKeepAlivePeriod(*srpcDefaultKeepAlivePeriod)
		if err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func dialHTTP(network, address string, tlsConfig *tls.Config,
	dialer Dialer) (*Client, error) {
	if *srpcProxy == "" {
		return dialHTTPDirect(network, address, tlsConfig, dialer)
	}
	var err error
	if d, ok := dialer.(*net.Dialer); ok {
		dialer, err = newProxyDialer(*srpcProxy, d)
	} else {
		dialer, err = newProxyDialer(*srpcProxy, &net.Dialer{})
	}
	if err != nil {
		return nil, err
	}
	return dialHTTPDirect(network, address, tlsConfig, dialer)
}

func dialHTTPDirect(network, address string, tlsConfig *tls.Config,
	dialer Dialer) (*Client, error) {
	insecureEndpoints := []endpointType{
		{&gobCoder{}, rpcPath, false},
		{&jsonCoder{}, jsonRpcPath, false},
	}
	secureEndpoints := []endpointType{
		{&gobCoder{}, tlsRpcPath, true},
		{&jsonCoder{}, jsonTlsRpcPath, true},
	}
	if tlsConfig == nil {
		return dialHTTPEndpoints(network, address, nil, false, dialer,
			insecureEndpoints)
	} else {
		var endpoints []endpointType
		endpoints = append(endpoints, secureEndpoints...)
		if tlsConfig.InsecureSkipVerify { // Don't have to trust server.
			endpoints = append(endpoints, insecureEndpoints...)
		}
		client, err := dialHTTPEndpoints(network, address, tlsConfig, false,
			dialer, endpoints)
		if err != nil &&
			strings.Contains(err.Error(), "malformed HTTP response") {
			// The server may do TLS on all connections: try that.
			return dialHTTPEndpoints(network, address, tlsConfig, true, dialer,
				secureEndpoints)
		}
		return client, err
	}
}

func dialHTTPEndpoint(network, address string, tlsConfig *tls.Config,
	fullTLS bool, dialer Dialer, endpoint endpointType) (*Client, error) {
	unsecuredConn, err := dial(network, address, dialer)
	if err != nil {
		return nil, err
	}
	dataConn := unsecuredConn
	doClose := true
	defer func() {
		if doClose {
			dataConn.Close()
		}
	}()
	if *srpcDefaultConnectTimeout > 0 {
		connectDeadline := time.Now().Add(*srpcDefaultConnectTimeout)
		if err := unsecuredConn.SetDeadline(connectDeadline); err != nil {
			return nil, err
		}
	}
	if fullTLS {
		tlsConn := tls.Client(unsecuredConn, tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			if strings.Contains(err.Error(), ErrorBadCertificate.Error()) {
				return nil, ErrorBadCertificate
			}
			return nil, err
		}
		dataConn = tlsConn
	}
	if err := doHTTPConnect(dataConn, endpoint.path); err != nil {
		return nil, err
	}
	if endpoint.tls && !fullTLS {
		tlsConn := tls.Client(unsecuredConn, tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			if strings.Contains(err.Error(), ErrorBadCertificate.Error()) {
				return nil, ErrorBadCertificate
			}
			return nil, err
		}
		dataConn = tlsConn
	}
	if *srpcDefaultConnectTimeout > 0 {
		if err := unsecuredConn.SetDeadline(time.Time{}); err != nil {
			return nil, err
		}
	}
	doClose = false
	return newClient(unsecuredConn, dataConn, endpoint.tls, endpoint.coderMaker)
}

func dialHTTPEndpoints(network, address string, tlsConfig *tls.Config,
	fullTLS bool, dialer Dialer, endpoints []endpointType) (*Client, error) {
	for _, endpoint := range endpoints {
		client, err := dialHTTPEndpoint(network, address, tlsConfig, fullTLS,
			dialer, endpoint)
		if err == nil {
			return client, nil
		}
		if err != ErrorNoSrpcEndpoint {
			return nil, err
		}
	}
	return nil, ErrorNoSrpcEndpoint
}

func doHTTPConnect(conn net.Conn, path string) error {
	var query string
	if *srpcClientDoNotUseMethodPowers {
		query = "?" + doNotUseMethodPowers + "=true"
	}
	io.WriteString(conn, "CONNECT "+path+query+" HTTP/1.0\n\n")
	// Require successful HTTP response before switching to SRPC protocol.
	resp, err := http.ReadResponse(bufio.NewReader(conn),
		&http.Request{Method: "CONNECT"})
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrorNoSrpcEndpoint
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrorBadCertificate
	}
	if resp.StatusCode == http.StatusMethodNotAllowed {
		return ErrorMissingCertificate
	}
	if resp.StatusCode != http.StatusOK || resp.Status != connectString {
		return errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil
}

func newClient(rawConn, dataConn net.Conn, isEncrypted bool,
	makeCoder coderMaker) (*Client, error) {
	clientMetricsMutex.Lock()
	numOpenClientConnections++
	clientMetricsMutex.Unlock()
	client := &Client{
		bufrw: bufio.NewReadWriter(bufio.NewReader(dataConn),
			bufio.NewWriter(dataConn)),
		conn:        dataConn,
		connType:    "unknown",
		localAddr:   rawConn.LocalAddr().String(),
		isEncrypted: isEncrypted,
		makeCoder:   makeCoder,
		remoteAddr:  rawConn.RemoteAddr().String(),
	}
	if tcpConn, ok := rawConn.(libnet.TCPConn); ok {
		client.tcpConn = tcpConn
		client.connType = "TCP"
	}
	if isEncrypted {
		client.connType += "/TLS"
	}
	if attemptTransportUpgrade && *srpcProxy == "" {
		oldBufrw := client.bufrw
		if _, err := client.localAttemptUpgradeToUnix(); err != nil {
			client.Close()
			return nil, err
		}
		if client.conn != dataConn && client.bufrw == oldBufrw {
			logger.Debugf(0,
				"transport type: %s did not replace buffer, fixing\n",
				client.connType)
			client.bufrw = bufio.NewReadWriter(bufio.NewReader(client.conn),
				bufio.NewWriter(client.conn))
		}
	}
	logger.Debugf(1, "made %s connection to: %s\n",
		client.connType, client.remoteAddr)
	return client, nil
}

func newFakeClient(options FakeClientOptions) *Client {
	return &Client{fakeClientOptions: &options}
}

func registerClientTlsConfig(config *tls.Config) {
	clientTlsConfig = config
	if config == nil {
		return
	}
	setupCertExpirationMetric(setupClientExpirationMetric, config,
		clientMetricsDir)
}

func (client *Client) call(serviceMethod string) (*Conn, error) {
	if client.conn == nil {
		panic("cannot call Client after Close()")
	}
	if client.resource != nil && !client.resource.inUse {
		panic("cannot call Client after Close() or Put()")
	}
	clientMetricsMutex.Lock()
	numOpenCallConnections++
	clientMetricsMutex.Unlock()
	client.callLock.Lock()
	conn, err := client.callWithLock(serviceMethod)
	if err != nil {
		client.callLock.Unlock()
		clientMetricsMutex.Lock()
		numOpenCallConnections--
		clientMetricsMutex.Unlock()
	}
	return conn, err
}

func (client *Client) callWithLock(serviceMethod string) (*Conn, error) {
	if client.timeout > 0 {
		deadline := time.Now().Add(client.timeout)
		if err := client.conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	_, err := client.bufrw.WriteString(serviceMethod + "\n")
	if err != nil {
		return nil, err
	}
	if err = client.bufrw.Flush(); err != nil {
		return nil, err
	}
	resp, err := client.bufrw.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if resp != "\n" {
		resp := resp[:len(resp)-1]
		if resp == ErrorAccessToMethodDenied.Error() {
			return nil, ErrorAccessToMethodDenied
		}
		return nil, errors.New(resp)
	}
	conn := &Conn{
		Decoder:     client.makeCoder.MakeDecoder(client.bufrw),
		Encoder:     client.makeCoder.MakeEncoder(client.bufrw),
		parent:      client,
		isEncrypted: client.isEncrypted,
		ReadWriter:  client.bufrw,
		remoteAddr:  client.remoteAddr,
	}
	return conn, nil
}

func (client *Client) close() error {
	if client.fakeClientOptions != nil {
		return nil
	}
	if client.conn == nil {
		return os.ErrClosed
	}
	client.bufrw.Flush()
	if client.resource == nil {
		clientMetricsMutex.Lock()
		numOpenCallConnections--
		numOpenClientConnections--
		clientMetricsMutex.Unlock()
		conn := client.conn
		client.conn = nil
		return conn.Close()
	}
	client.resource.resource.Release()
	client.conn = nil
	clientMetricsMutex.Lock()
	if client.resource.inUse {
		numInUseClientConnections--
		client.resource.inUse = false
	}
	numOpenCallConnections--
	numOpenClientConnections--
	clientMetricsMutex.Unlock()
	return client.resource.closeError
}

func (client *Client) ping() error {
	conn, err := client.call("")
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func (client *Client) requestReply(serviceMethod string, request interface{},
	reply interface{}) error {
	conn, err := client.Call(serviceMethod)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.requestReply(request, reply)
}

func (conn *Conn) requestReply(request interface{}, reply interface{}) error {
	if err := conn.Encode(request); err != nil {
		return err
	}
	if err := conn.Flush(); err != nil {
		return err
	}
	str, err := conn.ReadString('\n')
	if err != nil {
		return err
	}
	if str != "\n" {
		return errors.New(str[:len(str)-1])
	}
	return conn.Decode(reply)
}

func (client *Client) setKeepAlive(keepalive bool) error {
	if client.tcpConn == nil {
		return nil
	}
	return client.tcpConn.SetKeepAlive(keepalive)
}

func (client *Client) setKeepAlivePeriod(d time.Duration) error {
	if client.tcpConn == nil {
		return nil
	}
	return client.tcpConn.SetKeepAlivePeriod(d)
}

func (client *Client) setTimeout(timeout time.Duration) error {
	if timeout > 0 {
		client.timeout = timeout
		return nil
	}
	if client.timeout > 0 {
		return client.conn.SetDeadline(time.Time{})
	}
	return nil
}
