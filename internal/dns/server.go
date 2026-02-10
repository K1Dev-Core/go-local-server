package dns

import (
	"fmt"
	"net"
	"runtime/debug"
	"strings"

	"github.com/miekg/dns"
	"go-local-server/internal/config"
)

type Server struct {
	config  *config.AppConfig
	server  *dns.Server
	running bool
}

func NewServer(cfg *config.AppConfig) *Server {
	return &Server{
		config: cfg,
	}
}

func (s *Server) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	// Recover from any panics
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Printf("DNS handler panic: %v\n%s\n", rec, debug.Stack())
		}
	}()

	m := new(dns.Msg)
	m.SetReply(r)
	m.Compress = false

	for _, q := range r.Question {
		if s.isLocalDomain(q.Name) {
			s.addLocalRecord(m, q)
		}
	}

	if err := w.WriteMsg(m); err != nil {
		fmt.Printf("DNS write error: %v\n", err)
	}
}

func (s *Server) isLocalDomain(name string) bool {
	domain := strings.ToLower(strings.TrimSuffix(name, "."))
	return strings.HasSuffix(domain, "."+s.config.Domain) || domain == s.config.Domain
}

func (s *Server) addLocalRecord(m *dns.Msg, q dns.Question) {
	switch q.Qtype {
	case dns.TypeA:
		rr := &dns.A{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			A: net.ParseIP("127.0.0.1"),
		}
		m.Answer = append(m.Answer, rr)
	case dns.TypeAAAA:
		rr := &dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: dns.TypeAAAA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			AAAA: net.ParseIP("::1"),
		}
		m.Answer = append(m.Answer, rr)
	case dns.TypeSOA:
		soa := &dns.SOA{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: dns.TypeSOA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			Ns:      "ns.golocalserver.",
			Mbox:    "admin.golocalserver.",
			Serial:  2024010101,
			Refresh: 3600,
			Retry:   1800,
			Expire:  604800,
			Minttl:  86400,
		}
		m.Answer = append(m.Answer, soa)
	}
}

func (s *Server) Start() error {
	if s.running {
		return fmt.Errorf("DNS server already running")
	}

	dns.HandleFunc(".", s.handleDNSRequest)

	addr := fmt.Sprintf("127.0.0.1:%d", s.config.DNSPort)
	s.server = &dns.Server{Addr: addr, Net: "udp"}
	s.running = true

	go func() {
		// Recover from panics in the server goroutine
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Printf("DNS server panic: %v\n%s\n", rec, debug.Stack())
				s.running = false
			}
		}()

		if err := s.server.ListenAndServe(); err != nil {
			fmt.Printf("DNS server error: %v\n", err)
			s.running = false
		}
	}()

	return nil
}

func (s *Server) Stop() error {
	if !s.running {
		return nil
	}

	s.running = false
	if s.server != nil {
		return s.server.Shutdown()
	}
	return nil
}

func (s *Server) IsRunning() bool {
	return s.running
}
