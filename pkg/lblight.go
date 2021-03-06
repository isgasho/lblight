package pkg

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// Backend has the ReverseProxy to the real backend server.
type Backend struct {
	url          *url.URL // do we really need this here?
	Alive        bool
	InUse        bool
	mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

func NewBackend(uri string) *Backend {
	be := Backend{}
	var err error
	be.url, err = url.Parse(uri) // yes, ignoring error for moment... I'm bad. TODO(kpfaulkner)
	if err != nil {
		log.Fatalf("Unable to generate new backend....  intentionally dying")
	}

	be.Alive = false
	be.InUse = false
	be.ReverseProxy = httputil.NewSingleHostReverseProxy(be.url)
	//be.ReverseProxy.Transport = &http.Transport{DialTLS: dialTLS}
	return &be
}

// BackendRouter points to the REAL server doing the work, ie what the LB is connecting to.
// includes list of header values and/or url paths that will be accepted for this backend.
type BackendRouter struct {
	host string
	port int

	maxBackends int

	// if the beginning of the request is in acceptedPaths, then use this backend.
	acceptedPaths map[string]bool

	// if the header (key) in acceptedHeaders matches the value, then use this backend
	acceptedHeaders map[string]string

	// list of all backends that can be used with the config.
	backends []*Backend
}

func NewBackendRouter(host string, port int, acceptedHeaders map[string]string, acceptedPaths map[string]bool, maxBackends int) *BackendRouter {
	ber := BackendRouter{}
	ber.host = host
	ber.port = port
	ber.acceptedHeaders = acceptedHeaders
	ber.acceptedPaths = acceptedPaths
	ber.maxBackends = maxBackends
	return &ber
}

// GetBackend either retrieves backend from a pool OR adds new entry to pool (or errors out)
// TODO(kpfaulkner) add locking.
func (ber *BackendRouter) GetBackend() (*Backend, error ) {
	// check if we have any backends spare. If so, use it.
	for index,be := range ber.backends {
		if !be.InUse {
			ber.backends[index].InUse = true
			return be, nil
		}
	}

	// if none spare but haven't hit maxBackends yet, make one
	if len(ber.backends) <= ber.maxBackends {
		be := NewBackend(fmt.Sprintf("http://%s:%d", ber.host, ber.port))
		ber.backends = append(ber.backends, be)
		return be, nil
	}

	// if cant make any more, return error.
	return nil, fmt.Errorf("unable to provide backend for request")
}

// LBLight is the core of the load balancer.
// Listens to port, parses both headers and request paths and determines (based on configuration) where
// the request should be forwarded on to. All WIP and learning.
type LBLight struct {
	port int

	// match prefix to appropriate router
	pathPrefixToBackendRouter map[string]*BackendRouter

	// match header KEY to a potential router
	headerToBackendRouter map[string]map[string]*BackendRouter
}

func NewLBLight(port int) *LBLight {
	lbl := LBLight{}
	lbl.pathPrefixToBackendRouter = make(map[string]*BackendRouter)
	lbl.headerToBackendRouter = make(map[string]map[string]*BackendRouter)

	lbl.port = port
	return &lbl
}

// GetBackendRouterByExactPathPrefix returns the backend router which is registered for the exact
// match of "path". This is more for registration.
func (l *LBLight) GetBackendRouterByExactPathPrefix(path string) (*BackendRouter, error) {

	lowerPath := strings.ToLower(path)
	backend, ok := l.pathPrefixToBackendRouter[lowerPath]
	if ok {
		return backend, nil
	}

	return nil, fmt.Errorf("Unable to find matching backend for path %s", path)
}

// GetBackendRouterByPathPrefix Checks all routers that have been registered for path prefixes and
// searches each registered BackendRouter for a prefix match. This means it's NOT just a map lookup
// but iterating over all of them looking for prefix matches. May need to rethink this a bit.
func (l *LBLight) GetBackendRouterByPathPrefix(path string) (*BackendRouter, error) {
	lowerPath := strings.ToLower(path)
	for prefix, router := range l.pathPrefixToBackendRouter {
		if strings.HasPrefix(lowerPath, prefix) {
			return router, nil
		}
	}

	return nil, fmt.Errorf("Unable to find matching backend for path %s", path)
}


func (l *LBLight) GetBackendRouterByHeader(headerName string, headerValue string) (*BackendRouter, error) {

	headerValues, ok := l.headerToBackendRouter[headerName]
	if ok {
		// have a match for header... now check specific value.
		headerNameAndValueBackend, ok2 := headerValues[headerValue]
		if ok2 {
			return headerNameAndValueBackend, nil
		}
	}

	return nil, fmt.Errorf("Unable to find matching backend for header %s : %s", headerName, headerValue)
}

// AddBackendRouter register a BackendRouter to both pathPrefix map and header maps for lookup
// at runtime. If we have multiple, then we'd definitely NOT know who the request
// really should go to. If any of the paths/headers fail for thie BER, then fail them all.
func (l *LBLight) AddBackendRouter(ber *BackendRouter) error {

	// check if path/header already registered.
	if ber.acceptedPaths != nil {
		for path, _ := range ber.acceptedPaths {
			_, err := l.GetBackendRouterByExactPathPrefix(path)
			if err == nil {
				// no error, we already have something registered!
				return fmt.Errorf("Conflict: Backend path %s already registered", path)
			}
		}
	}

	// check headers.
	if ber.acceptedHeaders != nil {
		for header, val := range ber.acceptedHeaders {
			_, err2 := l.GetBackendRouterByHeader(header, val)
			if err2 == nil {
				// no error, we already have something registered!
				return fmt.Errorf("Conflict: Backend header %s : %s already registered", header, val)
			}
		}
	}

	// register valid paths/headers
	if ber.acceptedPaths != nil {
		for path, _ := range ber.acceptedPaths {
			l.pathPrefixToBackendRouter[path] = ber
		}
	}

	if ber.acceptedHeaders != nil {
		for header, val := range ber.acceptedHeaders {
			//l.headerToBackendRouter[header][val] = ber
			var specificHeaderMap map[string]*BackendRouter
			var ok bool
			specificHeaderMap,ok  = l.headerToBackendRouter[header]
			if !ok {
				//headerVal, ok2 := specificHeaderMap[val]
				specificHeaderMap = make(map[string]*BackendRouter)
			}
			specificHeaderMap[val] = ber
		}
	}

	return nil
}




// getBackend.... TODO(kpfaulkner) make real!
// just gets first match for now.
func (l *LBLight) getBackend(req *http.Request) (*Backend, error) {

	// just return first one
	backendRouter, err := l.GetBackendRouterByPathPrefix( req.URL.Path)
	if err != nil {
		return nil, err
	}

	// check if we have a backend for this router... if not, make one.
	backend, err := backendRouter.GetBackend()
	return backend, err

}

// handleRequestsAndRedirect determines which BackendRouter should be used for the incoming request.
func (l *LBLight) handleRequestsAndRedirect(res http.ResponseWriter, req *http.Request) {

	backend,err := l.getBackend(req)
	if err != nil {
		log.Errorf("Unable to find backend for URL %s", req.RequestURI)
		return
	}

	backend.ReverseProxy.ServeHTTP(res, req)
	return
}

func (l *LBLight) ListenAndServeTraffic() error {

	err := http.ListenAndServeTLS(fmt.Sprintf(":%d", l.port), "localhost.crt", "localhost.key", http.HandlerFunc(l.handleRequestsAndRedirect))
	if err != nil {
		log.Errorf("SERVER BLEW UP!! %s", err.Error())
	}
	return err
}
