package middleware

import (
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultLimit      = 30
	defaultMaxClients = 10000
)

type RateLimitConfig struct {
	Limit          int
	Window         time.Duration
	MaxClients     int
	TrustedProxies []netip.Prefix
	Now            func() time.Time
}

type clientWindow struct {
	attempts int
	reset    time.Time
}

type limiter struct {
	mu      sync.Mutex
	config  RateLimitConfig
	clients map[netip.Addr]clientWindow
}

func RateLimit(config RateLimitConfig) func(http.Handler) http.Handler {
	if config.Limit <= 0 {
		config.Limit = defaultLimit
	}
	if config.Window <= 0 {
		config.Window = time.Minute
	}
	if config.MaxClients <= 0 {
		config.MaxClients = defaultMaxClients
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	config.TrustedProxies = append([]netip.Prefix(nil), config.TrustedProxies...)
	limit := &limiter{config: config, clients: make(map[netip.Addr]clientWindow)}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			retry := limit.allow(clientIP(r, config.TrustedProxies))
			if retry > 0 {
				seconds := int64(retry / time.Second)
				if retry%time.Second != 0 {
					seconds++
				}
				w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":{"code":"rate_limited","message":"Too many requests"}}`+"\n")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (l *limiter) allow(ip netip.Addr) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.config.Now()
	window, exists := l.clients[ip]
	if exists && !now.Before(window.reset) {
		window = clientWindow{}
		exists = false
	}
	if !exists {
		// Keep active windows when full: eviction would let a client reset its limit.
		if len(l.clients) >= l.config.MaxClients {
			if retry := l.makeRoom(now); retry > 0 {
				return retry
			}
		}
		window = clientWindow{reset: now.Add(l.config.Window)}
	}
	if window.attempts >= l.config.Limit {
		return window.reset.Sub(now)
	}
	window.attempts++
	l.clients[ip] = window
	return 0
}

func (l *limiter) makeRoom(now time.Time) time.Duration {
	retry := l.config.Window
	for client, state := range l.clients {
		if !now.Before(state.reset) {
			delete(l.clients, client)
		} else if remaining := state.reset.Sub(now); remaining < retry {
			retry = remaining
		}
	}
	if len(l.clients) >= l.config.MaxClients {
		return retry
	}
	return 0
}

func clientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !trustedIP(peer, trusted) {
		return peer
	}
	client := peer
	// Parse only the trusted suffix: caller-controlled prefixes cannot change identity.
	for _, field := range slices.Backward(r.Header.Values("X-Forwarded-For")) {
		for {
			separator := strings.LastIndexByte(field, ',')
			address, parseErr := netip.ParseAddr(strings.TrimSpace(field[separator+1:]))
			if parseErr != nil || address.Zone() != "" {
				return peer
			}
			client = address.Unmap()
			if !trustedIP(client, trusted) {
				return client
			}
			if separator < 0 {
				break
			}
			field = field[:separator]
		}
	}
	return client
}

func trustedIP(ip netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
