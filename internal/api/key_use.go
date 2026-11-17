package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc/peer"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// keyUseInterval is how often one sender of one key is logged. Requests in
// between are counted and reported on the next line for that sender.
const keyUseInterval = 10 * time.Minute

// keyUseMaxSenders bounds the senders remembered between lines.
const keyUseMaxSenders = 1024

// keyUseLog logs which sender uses which API key, so a key whose owner is
// unknown can be traced to a user agent and a caller address. A sender is one
// key id with one user agent and one caller address. It logs the key's row id
// and never the key or its hash, at INFO.
type keyUseLog struct {
	logger *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	senders map[string]*keyUseEntry
}

type keyUseEntry struct {
	logged time.Time
	seen   int
}

func newKeyUseLog(logger *slog.Logger) *keyUseLog {
	return &keyUseLog{logger: logger, now: time.Now, senders: make(map[string]*keyUseEntry)}
}

// keyUseRequest is what a key-use line says about the caller. Origin and
// Referer name the page when the sender is a browser.
type keyUseRequest struct {
	userAgent string
	addr      string
	path      string
	origin    string
	referer   string
}

func newKeyUseRequest(userAgent, addr, path, origin, referer string) keyUseRequest {
	return keyUseRequest{
		userAgent: truncate(userAgent, rejectedHeaderMax),
		addr:      addr,
		path:      path,
		origin:    truncate(origin, rejectedHeaderMax),
		referer:   truncate(schemeAndHost(referer), rejectedHeaderMax),
	}
}

// use records one request made with a key and logs it unless the same sender
// was logged within keyUseInterval. A nil log does nothing.
func (l *keyUseLog) use(ident auth.Identity, req keyUseRequest) {
	if l == nil {
		return
	}
	id := fmt.Sprintf("%d|%s|%s", ident.KeyID, req.userAgent, req.addr)
	now := l.now()
	l.mu.Lock()
	e := l.senders[id]
	if e != nil && now.Sub(e.logged) < keyUseInterval {
		e.seen++
		l.mu.Unlock()
		return
	}
	seen := 0
	if e != nil {
		seen = e.seen
	}
	if e == nil && len(l.senders) >= keyUseMaxSenders {
		l.senders = make(map[string]*keyUseEntry)
	}
	l.senders[id] = &keyUseEntry{logged: now}
	l.mu.Unlock()
	l.logger.Info("api key used",
		"key_id", ident.KeyID,
		"project_id", ident.ProjectID,
		"scope", ident.Scope,
		"user_agent", req.userAgent,
		"caller_addr", req.addr,
		"path", req.path,
		"origin", req.origin,
		"referer", req.referer,
		"requests_since_last_line", seen,
	)
}

// peerHost is the transport peer's host of a gRPC call, or "" when unknown.
func peerHost(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
		return host
	}
	return p.Addr.String()
}
