package auth

import (
	"sync"
	"time"
)

type lookupLimiter struct {
	mu          sync.Mutex
	clients     map[string]rateLimit
	global      rateLimit
	clientLimit int
	globalLimit int
	maxClients  int
	window      time.Duration
}

func newLookupLimiter(clientLimit, globalLimit, maxClients int, window time.Duration) *lookupLimiter {
	return &lookupLimiter{
		clients: make(map[string]rateLimit), clientLimit: clientLimit, globalLimit: globalLimit,
		maxClients: maxClients, window: window,
	}
}

func (l *lookupLimiter) Allow(clientID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.global.start.IsZero() || now.Sub(l.global.start) >= l.window {
		l.global = rateLimit{start: now}
	}
	if l.global.count >= l.globalLimit {
		return false
	}
	client, exists := l.clients[clientID]
	if client.start.IsZero() || now.Sub(client.start) >= l.window {
		client = rateLimit{start: now}
		exists = false
	}
	if client.count >= l.clientLimit {
		return false
	}
	if !exists && len(l.clients) >= l.maxClients {
		for id, item := range l.clients {
			if now.Sub(item.start) >= l.window {
				delete(l.clients, id)
			}
		}
		if len(l.clients) >= l.maxClients {
			return false
		}
	}
	client.count++
	l.clients[clientID] = client
	l.global.count++
	return true
}
