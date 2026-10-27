package airplay

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
)

// Запасной поиск mDNS «legacy unicast» (RFC 6762 §6.7): запрос уходит на
// 224.0.0.251:5353 с эфемерного порта (не 5353), а ответ приходит прямо на этот
// порт. Для брандмауэра это ответ на исходящий запрос, поэтому отдельное
// разрешение для входящего UDP 5353 не нужно. Сервис собирается из нескольких
// пакетов: на каждое найденное имя отдельно спрашиваются SRV, TXT и адрес.

// unicastTarget — откуда и куда слать запросы на одном интерфейсе.
type unicastTarget struct {
	Iface   net.Interface // Index == 0 — интерфейс не задаётся (тесты)
	LocalIP net.IP        // IPv4-адрес интерфейса, к нему привязан сокет
	Dest    *net.UDPAddr  // обычно 224.0.0.251:5353
}

var mdnsMulticastV4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

const (
	unicastMaxAttempts = 6                      // повторов одного вопроса
	unicastRetryEvery  = 400 * time.Millisecond // пауза между повторами
)

// unicastBrowse ищет serviceType по всем целям до отмены ctx.
func unicastBrowse(ctx context.Context, serviceType string, targets []unicastTarget) ([]AirPlayDevice, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	type result struct {
		devs []AirPlayDevice
		err  error
	}
	results := make(chan result, len(targets))
	for _, t := range targets {
		go func(t unicastTarget) {
			devs, err := unicastBrowseOne(ctx, serviceType, t)
			results <- result{devs, err}
		}(t)
	}
	var all []AirPlayDevice
	var errs []string
	for range targets {
		r := <-results
		all = append(all, r.devs...)
		if r.err != nil {
			errs = append(errs, r.err.Error())
		}
	}
	if len(all) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return all, nil
}

// unicastStore — всё, что узнали из ответов на одном интерфейсе.
type unicastStore struct {
	mu        sync.Mutex
	service   string              // «_airplay._tcp.local.» в нижнем регистре
	instances map[string]string   // ключ (нижний регистр) -> имя как пришло
	srv       map[string]*dns.SRV // по имени экземпляра
	txt       map[string][]string // по имени экземпляра
	addrs     map[string][]net.IP // по имени хоста
	attempts  map[string]int      // «тип|имя» -> сколько раз спрашивали
	wake      chan struct{}
}

func newUnicastStore(serviceType string) *unicastStore {
	return &unicastStore{
		service:   strings.ToLower(dns.Fqdn(serviceType + ".local")),
		instances: map[string]string{},
		srv:       map[string]*dns.SRV{},
		txt:       map[string][]string{},
		addrs:     map[string][]net.IP{},
		attempts:  map[string]int{},
		wake:      make(chan struct{}, 1),
	}
}

// absorb запоминает все записи ответа (Answer, Authority, Additional).
func (s *unicastStore) absorb(msg *dns.Msg) {
	if !msg.Response {
		return
	}
	s.mu.Lock()
	changed := false
	rrs := append(append(append([]dns.RR{}, msg.Answer...), msg.Ns...), msg.Extra...)
	for _, rr := range rrs {
		name := strings.ToLower(rr.Header().Name)
		switch v := rr.(type) {
		case *dns.PTR:
			if name == s.service && v.Hdr.Ttl > 0 {
				key := strings.ToLower(v.Ptr)
				if _, ok := s.instances[key]; !ok {
					s.instances[key] = v.Ptr
					changed = true
				}
			}
		case *dns.SRV:
			if _, ok := s.srv[name]; !ok {
				s.srv[name] = v
				changed = true
			}
		case *dns.TXT:
			if _, ok := s.txt[name]; !ok {
				s.txt[name] = v.Txt
				changed = true
			}
		case *dns.A:
			changed = s.addAddr(name, v.A) || changed
		case *dns.AAAA:
			changed = s.addAddr(name, v.AAAA) || changed
		}
	}
	s.mu.Unlock()
	if changed {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *unicastStore) addAddr(host string, ip net.IP) bool {
	for _, have := range s.addrs[host] {
		if have.Equal(ip) {
			return false
		}
	}
	s.addrs[host] = append(s.addrs[host], append(net.IP(nil), ip...))
	return true
}

// unicastQuestion — вопрос о ещё не полученных данных.
type unicastQuestion struct {
	name  string
	qtype uint16
}

// pending возвращает вопросы, на которые ещё нет ответа, и учитывает попытку.
// Без retry — только те, что ещё ни разу не задавались (сразу после новых данных);
// с retry — повторы, пока не исчерпан лимит.
func (s *unicastStore) pending(retry bool) []unicastQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []unicastQuestion
	ask := func(name string, qtype uint16) {
		key := fmt.Sprintf("%d|%s", qtype, strings.ToLower(name))
		n := s.attempts[key]
		if n >= unicastMaxAttempts || (n > 0 && !retry) {
			return
		}
		s.attempts[key] = n + 1
		out = append(out, unicastQuestion{name, qtype})
	}
	for key, name := range s.instances {
		srv := s.srv[key]
		if srv == nil {
			ask(name, dns.TypeSRV)
		}
		if _, ok := s.txt[key]; !ok {
			ask(name, dns.TypeTXT)
		}
		if srv != nil {
			host := strings.ToLower(srv.Target)
			// link-local IPv6 без зоны не годится для подключения: с одним fe80::
			// адрес всё равно нужен, поэтому считаем только пригодные.
			if len(usableServiceIPs(s.addrs[host])) == 0 {
				ask(srv.Target, dns.TypeA)
				ask(srv.Target, dns.TypeAAAA)
			}
		}
	}
	return out
}

// devices собирает приёмники из всего, что накопилось (нужны SRV и адрес).
func (s *unicastStore) devices(serviceType string) []AirPlayDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AirPlayDevice
	for key, full := range s.instances {
		srv := s.srv[key]
		if srv == nil {
			continue
		}
		txt := map[string]string{}
		for _, item := range s.txt[key] {
			k, v, _ := strings.Cut(item, "=")
			txt[strings.ToLower(k)] = v
		}
		label := unescapeDNSName(instanceLabel(full, serviceType))
		if dev := deviceFromService(label, s.addrs[strings.ToLower(srv.Target)], int(srv.Port), txt); dev != nil {
			out = append(out, *dev)
		}
	}
	return out
}

func unicastBrowseOne(ctx context.Context, serviceType string, t unicastTarget) ([]AirPlayDevice, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: t.LocalIP, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("сокет на %s: %w", t.LocalIP, err)
	}
	defer conn.Close()
	if t.Iface.Index > 0 && t.Dest.IP.IsMulticast() {
		pc := ipv4.NewPacketConn(conn)
		if err := pc.SetMulticastInterface(&t.Iface); err != nil {
			return nil, fmt.Errorf("интерфейс %s: %w", t.Iface.Name, err)
		}
		_ = pc.SetMulticastTTL(255)
	}

	store := newUnicastStore(serviceType)
	go func() {
		<-ctx.Done()
		_ = conn.SetReadDeadline(time.Now())
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 9000)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				return
			}
			var msg dns.Msg
			if msg.Unpack(buf[:n]) == nil {
				store.absorb(&msg)
			}
		}
	}()

	send := func(questions ...unicastQuestion) {
		for _, q := range questions {
			msg := new(dns.Msg)
			msg.Id = dns.Id()
			msg.Question = []dns.Question{{Name: dns.Fqdn(q.name), Qtype: q.qtype, Qclass: dns.ClassINET}}
			if data, err := msg.Pack(); err == nil {
				_, _ = conn.WriteToUDP(data, t.Dest)
			}
		}
	}

	ptr := unicastQuestion{store.service, dns.TypePTR}
	send(ptr)
	for tick := 1; ; {
		select {
		case <-ctx.Done():
			<-readDone
			return store.devices(serviceType), nil
		case <-store.wake:
			send(store.pending(false)...)
		case <-time.After(unicastRetryEvery):
			// PTR повторяем и позже: первый запрос мог потеряться.
			if tick == 1 || tick == 3 || tick == 6 {
				send(ptr)
			}
			tick++
			send(store.pending(true)...)
		}
	}
}

// unicastTargets — по одной цели на каждый подходящий интерфейс с IPv4-адресом.
func unicastTargets(ifaces []net.Interface) []unicastTarget {
	var out []unicastTarget
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ip := ipFromAddr(a)
			if ip4 := ip.To4(); ip4 != nil && isUsableMDNSAddress(ip4) {
				out = append(out, unicastTarget{Iface: iface, LocalIP: ip4, Dest: mdnsMulticastV4})
				break
			}
		}
	}
	return out
}
