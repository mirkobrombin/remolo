// Package mdns implements just enough multicast DNS (RFC 6762) and DNS-SD
// (RFC 6763) for remolo to advertise and discover hosts on the local network
// segment, with no external mDNS daemon and no cgo.
//
// It advertises the service type "_remolo._udp.local." and answers PTR queries
// with the matching SRV, TXT and A records. Browse sends a PTR query and
// collects responses until a timeout elapses.
//
// The implementation is deliberately small: it speaks the wire format via
// golang.org/x/net/dns/dnsmessage and joins the IPv4 multicast group
// 224.0.0.251:5353. It is best-effort and lossy, like all of mDNS.
package mdns

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// ServiceName is the DNS-SD service type remolo advertises under.
	ServiceName = "_remolo._udp.local."

	multicastIPv4 = "224.0.0.251"
	mdnsPort      = 5353

	defaultTTL = 120 // seconds, per RFC 6762 recommendation for SRV/TXT/A
)

// multicastUDPAddr is the well-known IPv4 mDNS group endpoint.
var multicastUDPAddr = &net.UDPAddr{IP: net.ParseIP(multicastIPv4), Port: mdnsPort}

// Entry is a single discovered remolo instance.
type Entry struct {
	// Host is the advertised instance host name (the SRV target), for
	// example "myhost.local.".
	Host string
	// AddrPort is a dial-able "ip:port" if an A record and SRV port were
	// resolved, otherwise empty.
	AddrPort string
	// Txt carries the parsed key=value pairs from the TXT record.
	Txt map[string]string
}

// fqdn ensures name ends with a trailing dot.
func fqdn(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// localHostName returns a stable, mDNS-style host name for this machine,
// always ending in ".local.".
func localHostName() string {
	name, _ := osHostname()
	if name == "" {
		name = "remolo-host"
	}
	name = strings.TrimSuffix(name, ".local")
	return fqdn(name + ".local")
}

// listenMulticastUDP joins the mDNS group on every multicast-capable
// interface and returns a packet connection bound to it.
func listenMulticastUDP() (*net.UDPConn, error) {
	conn, err := net.ListenMulticastUDP("udp4", nil, multicastUDPAddr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// advertiser holds the running state of an Advertise call.
type advertiser struct {
	conn     *net.UDPConn
	instance string
	host     string
	port     uint16
	txt      map[string]string

	closeOnce sync.Once
	done      chan struct{}
}

// Advertise joins the mDNS group and answers queries for ServiceName with SRV,
// TXT and A records describing this host on the given UDP port. The returned
// io.Closer stops responding and releases the socket.
func Advertise(instance string, port uint16, txt map[string]string) (io.Closer, error) {
	conn, err := listenMulticastUDP()
	if err != nil {
		return nil, fmt.Errorf("mdns: join group: %w", err)
	}

	if txt == nil {
		txt = map[string]string{}
	}
	a := &advertiser{
		conn:     conn,
		instance: fqdn(instance),
		host:     localHostName(),
		port:     port,
		txt:      txt,
		done:     make(chan struct{}),
	}

	go a.serve()
	// Announce ourselves immediately (unsolicited response) so browsers that
	// are already listening pick us up without waiting for a query.
	a.announce()
	return a, nil
}

func (a *advertiser) Close() error {
	a.closeOnce.Do(func() {
		close(a.done)
		a.conn.Close()
	})
	return nil
}

func (a *advertiser) serve() {
	buf := make([]byte, 65536)
	for {
		select {
		case <-a.done:
			return
		default:
		}
		_ = a.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, _, err := a.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			// Socket closed or fatal error: stop.
			select {
			case <-a.done:
				return
			default:
				return
			}
		}
		a.handleQuery(buf[:n])
	}
}

// handleQuery parses an incoming message and, if it asks for our service,
// emits a response containing our records.
func (a *advertiser) handleQuery(msg []byte) {
	var p dnsmessage.Parser
	hdr, err := p.Start(msg)
	if err != nil {
		return
	}
	if hdr.Response {
		return // we only answer queries
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return
	}
	for _, q := range qs {
		name := q.Name.String()
		if (q.Type == dnsmessage.TypePTR && name == ServiceName) ||
			((q.Type == dnsmessage.TypeSRV || q.Type == dnsmessage.TypeTXT || q.Type == dnsmessage.TypeA) && name == a.instance) {
			a.announce()
			return
		}
	}
}

// announce builds and multicasts our full record set.
func (a *advertiser) announce() {
	resp, err := a.buildResponse()
	if err != nil {
		return
	}
	_, _ = a.conn.WriteToUDP(resp, multicastUDPAddr)
}

// buildResponse packs PTR + SRV + TXT + A answers for this advertiser.
func (a *advertiser) buildResponse() ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		Response:      true,
		Authoritative: true,
	})
	b.EnableCompression()

	if err := b.StartAnswers(); err != nil {
		return nil, err
	}

	serviceName, err := dnsmessage.NewName(ServiceName)
	if err != nil {
		return nil, err
	}
	instanceName, err := dnsmessage.NewName(a.instance)
	if err != nil {
		return nil, err
	}
	hostName, err := dnsmessage.NewName(a.host)
	if err != nil {
		return nil, err
	}

	// PTR: _remolo._udp.local. -> instance
	if err := b.PTRResource(dnsmessage.ResourceHeader{
		Name:  serviceName,
		Class: dnsmessage.ClassINET,
		TTL:   defaultTTL,
	}, dnsmessage.PTRResource{PTR: instanceName}); err != nil {
		return nil, err
	}

	// SRV: instance -> host:port
	if err := b.SRVResource(dnsmessage.ResourceHeader{
		Name:  instanceName,
		Class: dnsmessage.ClassINET,
		TTL:   defaultTTL,
	}, dnsmessage.SRVResource{
		Priority: 0,
		Weight:   0,
		Port:     a.port,
		Target:   hostName,
	}); err != nil {
		return nil, err
	}

	// TXT: instance -> key=value pairs
	if err := b.TXTResource(dnsmessage.ResourceHeader{
		Name:  instanceName,
		Class: dnsmessage.ClassINET,
		TTL:   defaultTTL,
	}, dnsmessage.TXTResource{TXT: encodeTXT(a.txt)}); err != nil {
		return nil, err
	}

	// A: host -> every non-loopback IPv4 we have
	for _, ip := range localIPv4s() {
		var a4 [4]byte
		copy(a4[:], ip.To4())
		if err := b.AResource(dnsmessage.ResourceHeader{
			Name:  hostName,
			Class: dnsmessage.ClassINET,
			TTL:   defaultTTL,
		}, dnsmessage.AResource{A: a4}); err != nil {
			return nil, err
		}
	}

	return b.Finish()
}

// Browse sends a PTR query for ServiceName and gathers responses until timeout
// (or ctx cancellation). Duplicate instances are merged by host name.
func Browse(ctx context.Context, timeout time.Duration) ([]Entry, error) {
	conn, err := listenMulticastUDP()
	if err != nil {
		return nil, fmt.Errorf("mdns: join group: %w", err)
	}
	defer conn.Close()

	query, err := buildQuery()
	if err != nil {
		return nil, err
	}
	if _, err := conn.WriteToUDP(query, multicastUDPAddr); err != nil {
		return nil, fmt.Errorf("mdns: send query: %w", err)
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	byInstance := map[string]*partial{}
	// host name -> resolved IPv4.
	hostIP := map[string]string{}

	buf := make([]byte, 65536)
	for {
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		_ = conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			break
		}
		parseResponse(buf[:n], byInstance, hostIP)
	}

	// Assemble entries.
	var entries []Entry
	for _, p := range byInstance {
		e := Entry{Host: p.host, Txt: p.txt}
		if ip, ok := hostIP[p.host]; ok && p.port != 0 {
			e.AddrPort = net.JoinHostPort(ip, fmt.Sprintf("%d", p.port))
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// buildQuery packs a PTR question for ServiceName.
func buildQuery() ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	name, err := dnsmessage.NewName(ServiceName)
	if err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{
		Name:  name,
		Type:  dnsmessage.TypePTR,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// partial is an instance entry assembled across several resource records.
type partial struct {
	host string
	port uint16
	txt  map[string]string
}

// parseResponse walks the answer section of a response and folds SRV/TXT/A
// records into the running maps. Unrelated names are ignored.
func parseResponse(msg []byte, byInstance map[string]*partial, hostIP map[string]string) {
	var p dnsmessage.Parser
	hdr, err := p.Start(msg)
	if err != nil {
		return
	}
	if !hdr.Response {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}

	get := func(instance string) *partial {
		if e, ok := byInstance[instance]; ok {
			return e
		}
		e := &partial{txt: map[string]string{}}
		byInstance[instance] = e
		return e
	}

	for {
		ah, err := p.AnswerHeader()
		if err != nil {
			break
		}
		switch ah.Type {
		case dnsmessage.TypePTR:
			r, err := p.PTRResource()
			if err != nil {
				return
			}
			if ah.Name.String() == ServiceName {
				get(r.PTR.String()) // ensure the instance exists
			}
		case dnsmessage.TypeSRV:
			r, err := p.SRVResource()
			if err != nil {
				return
			}
			e := get(ah.Name.String())
			e.host = r.Target.String()
			e.port = r.Port
		case dnsmessage.TypeTXT:
			r, err := p.TXTResource()
			if err != nil {
				return
			}
			e := get(ah.Name.String())
			for k, v := range decodeTXT(r.TXT) {
				e.txt[k] = v
			}
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				return
			}
			ip := net.IPv4(r.A[0], r.A[1], r.A[2], r.A[3])
			hostIP[ah.Name.String()] = ip.String()
		default:
			if err := p.SkipAnswer(); err != nil {
				return
			}
		}
	}
}

// encodeTXT turns a string map into the "key=value" slice TXT records use.
func encodeTXT(txt map[string]string) []string {
	if len(txt) == 0 {
		// A TXT record must have at least one (possibly empty) string.
		return []string{""}
	}
	out := make([]string, 0, len(txt))
	for k, v := range txt {
		out = append(out, k+"="+v)
	}
	return out
}

// decodeTXT parses "key=value" strings back into a map. Bare keys map to "".
func decodeTXT(items []string) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		if it == "" {
			continue
		}
		if i := strings.IndexByte(it, '='); i >= 0 {
			out[it[:i]] = it[i+1:]
		} else {
			out[it] = ""
		}
	}
	return out
}
