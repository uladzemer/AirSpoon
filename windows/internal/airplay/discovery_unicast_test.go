package airplay

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// testMDNSResponder — локальный «mDNS-ответчик» на loopback: отвечает на запросы
// unicast-клиентом (legacy unicast) на адрес и порт отправителя.
type testMDNSResponder struct {
	conn     *net.UDPConn
	instance string // «Телевизор._airplay._tcp.local.»
	host     string // «android-tv.local.»
	// allInOne — класть SRV, TXT и A в Additional ответа на PTR.
	allInOne bool

	mu      sync.Mutex
	asked   map[uint16]int // тип вопроса -> сколько раз спросили
	srcPort []int
}

func startTestResponder(t *testing.T, allInOne bool) *testMDNSResponder {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("нет loopback UDP: %v", err)
	}
	r := &testMDNSResponder{
		conn: conn, instance: "Телевизор._airplay._tcp.local.", host: "android-tv.local.",
		allInOne: allInOne, asked: map[uint16]int{},
	}
	t.Cleanup(func() { conn.Close() })
	go r.serve()
	return r
}

func (r *testMDNSResponder) records() (ptr, srv, txt, a dns.RR) {
	hdr := func(name string, typ uint16) dns.RR_Header {
		return dns.RR_Header{Name: name, Rrtype: typ, Class: dns.ClassINET, Ttl: 120}
	}
	ptr = &dns.PTR{Hdr: hdr("_airplay._tcp.local.", dns.TypePTR), Ptr: r.instance}
	srv = &dns.SRV{Hdr: hdr(r.instance, dns.TypeSRV), Port: 7000, Target: r.host}
	txt = &dns.TXT{Hdr: hdr(r.instance, dns.TypeTXT), Txt: []string{
		"deviceid=6a:be:df:18:3e:82", "features=0x5A7FFEF7,0x40", "model=AppleTV3,2", "srcvers=220.68",
	}}
	a = &dns.A{Hdr: hdr(r.host, dns.TypeA), A: net.IPv4(192, 168, 50, 59)}
	return
}

func (r *testMDNSResponder) serve() {
	buf := make([]byte, 9000)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var q dns.Msg
		if q.Unpack(buf[:n]) != nil || len(q.Question) == 0 {
			continue
		}
		r.mu.Lock()
		r.asked[q.Question[0].Qtype]++
		r.srcPort = append(r.srcPort, from.Port)
		r.mu.Unlock()

		ptr, srv, txt, a := r.records()
		resp := new(dns.Msg)
		resp.Id = q.Id
		resp.Response = true
		resp.Authoritative = true
		resp.Question = q.Question
		switch q.Question[0].Qtype {
		case dns.TypePTR:
			resp.Answer = []dns.RR{ptr}
			if r.allInOne {
				resp.Extra = []dns.RR{srv, txt, a}
			}
		case dns.TypeSRV:
			resp.Answer = []dns.RR{srv}
		case dns.TypeTXT:
			resp.Answer = []dns.RR{txt}
		case dns.TypeA:
			resp.Answer = []dns.RR{a}
		default:
			continue
		}
		if data, err := resp.Pack(); err == nil {
			_, _ = r.conn.WriteToUDP(data, from)
		}
	}
}

func (r *testMDNSResponder) target() unicastTarget {
	return unicastTarget{LocalIP: net.IPv4(127, 0, 0, 1), Dest: r.conn.LocalAddr().(*net.UDPAddr)}
}

func checkTestDevice(t *testing.T, devs []AirPlayDevice) {
	t.Helper()
	if len(devs) != 1 {
		t.Fatalf("найдено %d приёмников, нужен 1: %+v", len(devs), devs)
	}
	d := devs[0]
	if d.Name != "Телевизор" || d.IP != "192.168.50.59" || d.Port != 7000 || d.DeviceID != "6a:be:df:18:3e:82" ||
		d.Model != "AppleTV3,2" || d.SourceVersion != "220.68" || !d.SupportsScreen() {
		t.Fatalf("приёмник собран неверно: %+v", d)
	}
}

// Ответчик отдаёт только PTR: SRV, TXT и адрес клиент должен спросить отдельно
// (сборка из одного пакета, как в zeroconf, здесь не сработала бы).
func TestUnicastBrowseResolvesInstanceWithFollowUpQueries(t *testing.T) {
	r := startTestResponder(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	devs, err := unicastBrowse(ctx, "_airplay._tcp", []unicastTarget{r.target()})
	if err != nil {
		t.Fatal(err)
	}
	checkTestDevice(t, devs)

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, typ := range []uint16{dns.TypePTR, dns.TypeSRV, dns.TypeTXT, dns.TypeA} {
		if r.asked[typ] == 0 {
			t.Errorf("не было вопроса типа %s", dns.TypeToString[typ])
		}
	}
	for _, p := range r.srcPort {
		if p == 5353 {
			t.Fatal("запрос ушёл с порта 5353: это не legacy unicast")
		}
	}
}

// Всё в одном ответе: лишних запросов SRV/TXT/A не нужно.
func TestUnicastBrowseUsesAdditionalRecords(t *testing.T) {
	r := startTestResponder(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	devs, err := unicastBrowse(ctx, "_airplay._tcp", []unicastTarget{r.target()})
	if err != nil {
		t.Fatal(err)
	}
	checkTestDevice(t, devs)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.asked[dns.TypeSRV]+r.asked[dns.TypeTXT]+r.asked[dns.TypeA] != 0 {
		t.Errorf("лишние запросы при полном ответе: %v", r.asked)
	}
}

func TestUnicastBrowseWithoutResponderReturnsNothing(t *testing.T) {
	// Порт без слушателя: ответов нет, ошибки тоже нет.
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skip(err)
	}
	dest := conn.LocalAddr().(*net.UDPAddr)
	conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	devs, err := unicastBrowse(ctx, "_airplay._tcp", []unicastTarget{{LocalIP: net.IPv4(127, 0, 0, 1), Dest: dest}})
	if err != nil || len(devs) != 0 {
		t.Fatalf("devs=%v err=%v", devs, err)
	}
}

func TestUnicastStoreIgnoresGoodbyeAndRequests(t *testing.T) {
	s := newUnicastStore("_airplay._tcp")
	bye := new(dns.Msg)
	bye.Response = true
	bye.Answer = []dns.RR{&dns.PTR{Hdr: dns.RR_Header{Name: "_airplay._tcp.local.", Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 0}, Ptr: "Ушёл._airplay._tcp.local."}}
	s.absorb(bye)
	req := new(dns.Msg) // не ответ: Response == false
	req.Answer = []dns.RR{&dns.PTR{Hdr: dns.RR_Header{Name: "_airplay._tcp.local.", Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 120}, Ptr: "Чужой._airplay._tcp.local."}}
	s.absorb(req)
	if len(s.instances) != 0 {
		t.Fatalf("прощание и запросы не должны давать экземпляры: %v", s.instances)
	}
	if q := s.pending(true); len(q) != 0 {
		t.Fatalf("вопросов быть не должно: %v", q)
	}
}

func TestUnicastTargetsPicksOneIPv4PerInterface(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip(err)
	}
	targets := unicastTargets(ifaces)
	seen := map[int]bool{}
	for _, tg := range targets {
		if tg.LocalIP.To4() == nil || seen[tg.Iface.Index] || !tg.Dest.IP.Equal(net.IPv4(224, 0, 0, 251)) || tg.Dest.Port != 5353 {
			t.Fatalf("неверная цель: %+v", tg)
		}
		seen[tg.Iface.Index] = true
	}
}

// Пришёл только fe80:: (без зоны): для подключения он не годится, поэтому
// A/AAAA надо спросить, иначе устройство теряется.
func TestUnicastStoreAsksAddressesWhenOnlyLinkLocalIPv6(t *testing.T) {
	s := newUnicastStore("_airplay._tcp")
	s.instances["tv"] = "ТВ._airplay._tcp.local."
	s.srv["tv"] = &dns.SRV{Target: "tv.local.", Port: 7000}
	s.txt["tv"] = []string{"deviceid=aa"}
	s.addAddr("tv.local.", net.ParseIP("fe80::1"))
	var types []uint16
	for _, q := range s.pending(true) {
		types = append(types, q.qtype)
	}
	if len(types) != 2 {
		t.Fatalf("с одним fe80:: нужны запросы A и AAAA, получено %v", types)
	}
	// Как только есть пригодный адрес, вопросов больше нет.
	s2 := newUnicastStore("_airplay._tcp")
	s2.instances["tv"] = "ТВ._airplay._tcp.local."
	s2.srv["tv"] = &dns.SRV{Target: "tv.local.", Port: 7000}
	s2.txt["tv"] = []string{"deviceid=aa"}
	s2.addAddr("tv.local.", net.ParseIP("fe80::1"))
	s2.addAddr("tv.local.", net.ParseIP("192.168.1.5"))
	if q := s2.pending(true); len(q) != 0 {
		t.Fatalf("с IPv4 вопросов быть не должно: %v", q)
	}
}
