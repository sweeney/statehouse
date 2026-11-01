package main

// store.go: one JSON file, rewritten atomically. A household has a handful of
// users and a few devices each; SQLite would be a dependency for nothing.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type KindPref struct {
	Enabled bool `json:"enabled"`
	// Only narrows the kind to these subjects (device ids, UPS labels). Empty = all.
	Only []string `json:"only,omitempty"`
}

type QuietHours struct {
	Start string `json:"start"` // "22:30", local to the service's timezone
	End   string `json:"end"`   // "07:00"; may wrap midnight
	// Ring lists kinds that ignore quiet hours. Nil means the catalog's
	// BypassQuiet defaults (UPS alarms); an empty list means none do.
	Ring []Kind `json:"ring,omitempty"`
}

type Prefs struct {
	Kinds map[Kind]KindPref `json:"kinds"`
	Quiet *QuietHours       `json:"quiet,omitempty"`
}

func DefaultPrefs() Prefs {
	p := Prefs{Kinds: map[Kind]KindPref{}}
	for _, c := range Catalog {
		p.Kinds[c.Kind] = KindPref{Enabled: c.DefaultOn}
	}
	return p
}

// Wants applies one user's preferences to one notification.
func (p Prefs) Wants(n Notification, now time.Time) bool {
	kp, ok := p.Kinds[n.Kind]
	if !ok {
		c, _ := catalogEntry(n.Kind)
		kp = KindPref{Enabled: c.DefaultOn}
	}
	if !kp.Enabled {
		return false
	}
	if len(kp.Only) > 0 && !contains(kp.Only, n.Subject) {
		return false
	}
	if p.Quiet != nil && inQuiet(*p.Quiet, now) {
		if p.Quiet.Ring != nil {
			return containsKind(p.Quiet.Ring, n.Kind)
		}
		c, _ := catalogEntry(n.Kind)
		return c.BypassQuiet
	}
	return true
}

func inQuiet(q QuietHours, now time.Time) bool {
	start, err1 := time.Parse("15:04", q.Start)
	end, err2 := time.Parse("15:04", q.End)
	if err1 != nil || err2 != nil {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	s, e := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
	if s <= e {
		return m >= s && m < e
	}
	return m >= s || m < e // wraps midnight
}

func containsKind(list []Kind, k Kind) bool {
	for _, x := range list {
		if x == k {
			return true
		}
	}
	return false
}

// Subscription is one browser/device's PushSubscription.toJSON(), plus bookkeeping.
type Subscription struct {
	ID       string    `json:"id"`
	Endpoint string    `json:"endpoint"`
	P256dh   string    `json:"p256dh"`
	Auth     string    `json:"auth"`
	Label    string    `json:"label"` // "Martin's iPhone", derived from UA if unset
	Created  time.Time `json:"created"`
	// LastSeen is refreshed every time the app opens and re-posts its
	// subscription. iOS has been seen to drop subscriptions silently and never
	// fires pushsubscriptionchange, so "app opened recently" is the only
	// liveness evidence a 201 from Apple cannot give.
	LastSeen time.Time `json:"last_seen"`
	LastOK   time.Time `json:"last_ok,omitzero"`
	LastErr  string    `json:"last_err,omitempty"`
}

type User struct {
	ID       string          `json:"id"`
	Username string          `json:"username"`
	Prefs    Prefs           `json:"prefs"`
	Subs     []*Subscription `json:"subs"`
}

type storeData struct {
	Users map[string]*User     `json:"users"`
	Seen  map[string]time.Time `json:"seen"` // dedupe keys → when sent
}

type Store struct {
	mu   sync.Mutex
	path string
	d    storeData
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, d: storeData{Users: map[string]*User{}, Seen: map[string]time.Time{}}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.d); err != nil {
		return nil, err
	}
	if s.d.Users == nil {
		s.d.Users = map[string]*User{}
	}
	if s.d.Seen == nil {
		s.d.Seen = map[string]time.Time{}
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) userLocked(id, username string) *User {
	u, ok := s.d.Users[id]
	if !ok {
		u = &User{ID: id, Username: username, Prefs: DefaultPrefs()}
		s.d.Users[id] = u
	}
	if username != "" {
		u.Username = username
	}
	return u
}

// User returns a copy, creating the record with default prefs on first sight.
func (s *Store) User(id, username string) User {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.userLocked(id, username)
	cp := *u
	cp.Subs = append([]*Subscription(nil), u.Subs...)
	return cp
}

func (s *Store) SetPrefs(id string, p Prefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userLocked(id, "").Prefs = p
	return s.saveLocked()
}

func subID(endpoint string) string {
	h := sha256.Sum256([]byte(endpoint))
	return base64.RawURLEncoding.EncodeToString(h[:9])
}

// Upsert adds or refreshes a subscription. An endpoint belongs to one user:
// a shared iPad that signs in as someone else moves to them.
func (s *Store) Upsert(userID string, sub Subscription, now time.Time) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub.ID = subID(sub.Endpoint)
	for uid, u := range s.d.Users {
		for i, x := range u.Subs {
			if x.ID != sub.ID {
				continue
			}
			if uid == userID {
				x.P256dh, x.Auth, x.LastSeen = sub.P256dh, sub.Auth, now
				if sub.Label != "" {
					x.Label = sub.Label
				}
				return x, s.saveLocked()
			}
			u.Subs = append(u.Subs[:i], u.Subs[i+1:]...)
			break
		}
	}
	sub.Created, sub.LastSeen = now, now
	u := s.userLocked(userID, "")
	u.Subs = append(u.Subs, &sub)
	return &sub, s.saveLocked()
}

func (s *Store) RemoveSub(userID, subID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.d.Users[userID]
	if !ok {
		return false
	}
	for i, x := range u.Subs {
		if x.ID == subID {
			u.Subs = append(u.Subs[:i], u.Subs[i+1:]...)
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

func (s *Store) RecordResult(subID string, ok bool, errMsg string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.d.Users {
		for _, x := range u.Subs {
			if x.ID == subID {
				if ok {
					x.LastOK, x.LastErr = now, ""
				} else {
					x.LastErr = errMsg
				}
			}
		}
	}
	_ = s.saveLocked()
}

// Users returns a snapshot for fan-out.
func (s *Store) Users() []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, 0, len(s.d.Users))
	for _, u := range s.d.Users {
		cp := *u
		cp.Subs = nil
		for _, x := range u.Subs {
			c := *x
			cp.Subs = append(cp.Subs, &c)
		}
		out = append(out, cp)
	}
	return out
}

// FirstSighting records a dedupe key and reports whether it is new. Keys older
// than a week are pruned; nothing in the rules re-emits a key that late.
func (s *Store) FirstSighting(key string, now time.Time) bool {
	if key == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.d.Seen[key]; seen {
		return false
	}
	for k, t := range s.d.Seen {
		if now.Sub(t) > 7*24*time.Hour {
			delete(s.d.Seen, k)
		}
	}
	s.d.Seen[key] = now
	_ = s.saveLocked()
	return true
}
