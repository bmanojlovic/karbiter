package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const maxNamespaces = 65536

type Lease struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	Metadata   LeaseMetadata `json:"metadata"`
	Spec       LeaseSpec     `json:"spec"`
}

type LeaseMetadata struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	ResourceVersion string `json:"resourceVersion"`
}

type LeaseSpec struct {
	HolderIdentity       *string `json:"holderIdentity,omitempty"`
	LeaseDurationSeconds *int    `json:"leaseDurationSeconds,omitempty"`
	AcquireTime          *string `json:"acquireTime,omitempty"`
	RenewTime            *string `json:"renewTime,omitempty"`
	LeaseTransitions     *int    `json:"leaseTransitions,omitempty"`
}

type namespace struct {
	leases   map[string]*Lease
	rv       int64
	lastSeen atomic.Int64
}

type server struct {
	mu         sync.RWMutex
	namespaces map[string]*namespace // key: token
}

func main() {
	addr := flag.String("addr", ":9443", "Listen address")
	certFile := flag.String("cert", "", "TLS certificate file")
	keyFile := flag.String("key", "", "TLS key file")
	flag.Parse()

	s := &server{namespaces: make(map[string]*namespace)}
	go s.reaper()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/apis/coordination.k8s.io/v1/namespaces/", s.handleLease)
	mux.HandleFunc("/api", s.handleAPIDiscovery)
	mux.HandleFunc("/apis", s.handleAPIsDiscovery)
	mux.HandleFunc("/apis/coordination.k8s.io/v1", s.handleCoordinationDiscovery)

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if *certFile != "" && *keyFile != "" {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		log.Printf("karbiter listening on %s (TLS)", *addr)
		log.Fatal(srv.ListenAndServeTLS(*certFile, *keyFile))
	}
	log.Printf("karbiter listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}

func tokenFromRequest(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}

func (s *server) getOrCreateNS(token string) (*namespace, error) {
	if ns, ok := s.namespaces[token]; ok {
		ns.lastSeen.Store(time.Now().UnixNano())
		return ns, nil
	}
	if len(s.namespaces) >= maxNamespaces {
		return nil, fmt.Errorf("namespace limit reached (%d)", maxNamespaces)
	}
	ns := &namespace{leases: make(map[string]*Lease)}
	ns.lastSeen.Store(time.Now().UnixNano())
	s.namespaces[token] = ns
	return ns, nil
}

func (s *server) reaper() {
	for range time.Tick(30 * time.Second) {
		cutoff := time.Now().Add(-time.Minute).UnixNano()
		s.mu.Lock()
		for token, ns := range s.namespaces {
			if ns.lastSeen.Load() < cutoff {
				delete(s.namespaces, token)
			}
		}
		s.mu.Unlock()
	}
}

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	n := len(s.namespaces)
	s.mu.RUnlock()
	fmt.Fprintf(w, "ok %d/%d", n, maxNamespaces)
}

func (s *server) handleLease(w http.ResponseWriter, r *http.Request) {
	token := tokenFromRequest(r)
	if token == "" {
		writeStatus(w, http.StatusUnauthorized, "Bearer token required")
		return
	}

	const prefix = "/apis/coordination.k8s.io/v1/namespaces/"
	path := r.URL.Path[len(prefix):]

	var ns, name string
	for i := range path {
		if path[i] == '/' {
			ns = path[:i]
			if rest := path[i+1:]; len(rest) > 7 && rest[:7] == "leases/" {
				name = rest[7:]
			}
			break
		}
	}
	if ns == "" || name == "" {
		writeStatus(w, http.StatusNotFound, "lease name required")
		return
	}

	key := ns + "/" + name

	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		tns := s.namespaces[token]
		if tns != nil {
			tns.lastSeen.Store(time.Now().UnixNano())
		}
		s.mu.RUnlock()
		if tns == nil {
			writeStatus(w, http.StatusNotFound, fmt.Sprintf("leases.coordination.k8s.io %q not found", name))
			return
		}
		lease, ok := tns.leases[key]
		if !ok {
			writeStatus(w, http.StatusNotFound, fmt.Sprintf("leases.coordination.k8s.io %q not found", name))
			return
		}
		writeJSON(w, http.StatusOK, lease)

	case http.MethodPost:
		var l Lease
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			writeStatus(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		tns, err := s.getOrCreateNS(token)
		if err != nil {
			s.mu.Unlock()
			writeStatus(w, http.StatusInsufficientStorage, err.Error())
			return
		}
		if _, ok := tns.leases[key]; ok {
			s.mu.Unlock()
			writeStatus(w, http.StatusConflict, "already exists")
			return
		}
		tns.rv++
		l.APIVersion = "coordination.k8s.io/v1"
		l.Kind = "Lease"
		l.Metadata.Name = name
		l.Metadata.Namespace = ns
		l.Metadata.ResourceVersion = fmt.Sprintf("%d", tns.rv)
		tns.leases[key] = &l
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, &l)

	case http.MethodPut:
		var l Lease
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			writeStatus(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		tns := s.namespaces[token]
		if tns == nil {
			s.mu.Unlock()
			writeStatus(w, http.StatusNotFound, "not found")
			return
		}
		existing, ok := tns.leases[key]
		if !ok {
			s.mu.Unlock()
			writeStatus(w, http.StatusNotFound, "not found")
			return
		}
		if l.Metadata.ResourceVersion != existing.Metadata.ResourceVersion {
			s.mu.Unlock()
			writeStatus(w, http.StatusConflict, "resourceVersion conflict")
			return
		}
		tns.rv++
		l.Metadata.Name = existing.Metadata.Name
		l.Metadata.Namespace = existing.Metadata.Namespace
		l.Metadata.ResourceVersion = fmt.Sprintf("%d", tns.rv)
		l.APIVersion = "coordination.k8s.io/v1"
		l.Kind = "Lease"
		tns.leases[key] = &l
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, &l)

	default:
		writeStatus(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) handleAPIDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"kind": "APIVersions", "versions": []string{"v1"}})
}

func (s *server) handleAPIsDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"kind": "APIGroupList", "apiVersion": "v1",
		"groups": []map[string]interface{}{{
			"name":             "coordination.k8s.io",
			"versions":         []map[string]string{{"groupVersion": "coordination.k8s.io/v1", "version": "v1"}},
			"preferredVersion": map[string]string{"groupVersion": "coordination.k8s.io/v1", "version": "v1"},
		}},
	})
}

func (s *server) handleCoordinationDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"kind": "APIResourceList", "apiVersion": "v1", "groupVersion": "coordination.k8s.io/v1",
		"resources": []map[string]interface{}{{
			"name": "leases", "namespaced": true, "kind": "Lease",
			"verbs": []string{"create", "get", "update"},
		}},
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeStatus(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"kind": "Status", "apiVersion": "v1", "status": "Failure", "message": msg, "code": code,
	})
}
