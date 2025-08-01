package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/clarketm/json"
	"github.com/coreos/go-semver/semver"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const (
	// SecurePort is the tls secured port to serve ignition configs
	SecurePort = 22623
	// InsecurePort is the port to serve ignition configs w/o tls
	InsecurePort = 22624
)

type poolRequest struct {
	machineConfigPool string
	version           *semver.Version
}

// APIServer provides the HTTP(s) endpoint
// for providing the machine configs.
type APIServer struct {
	handler   http.Handler
	port      int
	insecure  bool
	cert      string
	key       string
	tlsConfig *tls.Config
}

// NewAPIServer initializes a new API server
// that runs the Machine Config Server as a
// handler.
func NewAPIServer(a *APIHandler, p int, is bool, c, k string, t *tls.Config) *APIServer {
	mux := http.NewServeMux()
	mux.Handle("/config/", a)
	mux.Handle("/healthz", &healthHandler{})
	mux.Handle("/", &defaultHandler{})

	return &APIServer{
		handler:   mux,
		port:      p,
		insecure:  is,
		cert:      c,
		key:       k,
		tlsConfig: t,
	}
}

// Serve launches the API Server.
func (a *APIServer) Serve() {
	mcs := getHTTPServerCfg(fmt.Sprintf(":%v", a.port), a.handler, a.tlsConfig)

	klog.Infof("Launching server on %s", mcs.Addr)
	if a.insecure {
		// Serve a non TLS server.
		if err := mcs.ListenAndServe(); err != http.ErrServerClosed {
			klog.Exitf("Machine Config Server exited with error: %v", err)
		}
	} else {
		certWatcher, err := certwatcher.New(a.cert, a.key)
		if err != nil {
			klog.Exitf("failed to load serving cert: %v", err)
		}

		mcs.TLSConfig.GetCertificate = certWatcher.GetCertificate

		go func() {
			log.SetLogger(klog.NewKlogr())
			if err := certWatcher.Start(context.Background()); err != nil {
				klog.Fatalf("Certificate watcher failed to start: %v", err)
			}
		}()

		if err := mcs.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			klog.Exitf("Machine Config Server exited with error: %v", err)
		}
	}
}

// APIHandler is the HTTP Handler for the
// Machine Config Server.
type APIHandler struct {
	server Server
}

// NewServerAPIHandler initializes a new API handler
// for the Machine Config Server.
func NewServerAPIHandler(s Server) *APIHandler {
	return &APIHandler{
		server: s,
	}
}

// ServeHTTP handles the requests for the machine config server
// API handler.
func (sh *APIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path == "" {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	poolName := path.Base(r.URL.Path)
	useragent := r.Header.Get("User-Agent")
	acceptHeader := r.Header.Get("Accept")
	klog.Infof("Pool %q requested by address:%q User-Agent:%q Accept-Header: %q", poolName, r.RemoteAddr, useragent, acceptHeader)

	reqConfigVer, err := detectSpecVersionFromAcceptHeader(acceptHeader)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusBadRequest)
		klog.Error(err.Error())
		return
	}

	cr := poolRequest{
		machineConfigPool: poolName,
		version:           reqConfigVer,
	}

	conf, err := sh.server.GetConfig(cr)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusInternalServerError)
		klog.Errorf("couldn't get config for req: %+v, error: %v", cr, err)
		return
	}
	if conf == nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNotFound)
		return
	}

	serveConf, err := ctrlcommon.ConvertRawExtIgnitionToVersion(conf, *reqConfigVer)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusInternalServerError)
		klog.Errorf("couldn't convert config for req: %v, error: %v", cr, err)
		return
	}

	data, err := json.Marshal(&serveConf)
	if err != nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusInternalServerError)
		klog.Errorf("failed to marshal %v config: %v", cr, err)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	_, err = w.Write(data)
	if err != nil {
		klog.Errorf("failed to write %v response: %v", cr, err)
	}
}

type healthHandler struct{}

type acceptHeaderValue struct {
	MIMEType    string
	MIMESubtype string
	SemVer      *semver.Version
	QValue      *float32
}

// Parse an accept header, ignoring any extensions that aren't
// either version or relative quality factor q.
func parseAcceptHeader(input string) ([]acceptHeaderValue, error) {
	var header []acceptHeaderValue

	values := strings.Split(input, ",")
	for _, value := range values {
		// remove spaces
		value = strings.TrimSpace(value)
		parts := strings.Split(value, ";")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}

		if !strings.Contains(parts[0], "/") {
			// This is not a MIME type, ignore bad data
			continue
		}
		// mtype[0] is the main MIME type, mtype[1] is the sub MIME type
		mtype := strings.SplitN(parts[0], "/", 2)

		// check value extensions for version and q parameters, ignore other extensions
		var v *semver.Version
		var q *float32
		for _, ext := range parts[1:] {
			if strings.Contains(ext, "=") {
				keyval := strings.SplitN(ext, "=", 2)
				if keyval[0] == "version" && v == nil {
					var err error
					v, err = semver.NewVersion(keyval[1])
					if err != nil {
						// This is not a valid version
						continue
					}
				} else if keyval[0] == "q" && q == nil {
					q64, err := strconv.ParseFloat(keyval[1], 32)
					if err != nil {
						// This is not a valid relative quality factor
						continue
					}
					qval := float32(q64)
					q = &qval
				}
			}
		}

		// Default q to 1
		if q == nil {
			q1 := float32(1.0)
			q = &q1
		}

		header = append(header, acceptHeaderValue{
			mtype[0],
			mtype[1],
			v,
			q,
		})
	}

	if len(header) == 0 {
		return nil, fmt.Errorf("no valid accept header detected")
	}

	// Sort headers by descending q factor value.
	// This is the order of precedence any application
	// that receives this header should operate with.
	sort.SliceStable(header, func(i, j int) bool { return *header[i].QValue > *header[j].QValue })

	return header, nil
}

// detectSpecVersionFromAcceptHeaderUseragent returns a supported Ignition config spec version for a given Accept header.
// For non-Ignition Accept headers it defaults to config spec v2.2.0
func detectSpecVersionFromAcceptHeader(acceptHeader string) (*semver.Version, error) {
	// for now, serve v2 if we receive a request without an Ignition accept header.
	// This happens if the user pings the endpoint directly (e.g. with curl)
	// and we don't want to break existing behaviour.
	// For Ignition v0.x, the accept header looks like:
	// "application/vnd.coreos.ignition+json; version=2.4.0, application/vnd.coreos.ignition+json; version=1; q=0.5, */*; q=0.1".
	// For v2.x, it looks like:
	// "application/vnd.coreos.ignition+json;version=3.2.0, */*;q=0.1".

	v22Version := semver.New("2.2.0")
	headers, err := parseAcceptHeader(acceptHeader)
	if err != nil {
		// no valid accept headers detected at all, serve default
		return v22Version, nil
	}

	for _, header := range headers {
		if header.MIMESubtype != "vnd.coreos.ignition+json" || header.SemVer == nil {
			continue
		}

		reqVersion := header.SemVer
		// If the version is > 2.2, but it's still a v2 -> Assume 2.2 handling
		if reqVersion.Compare(*v22Version) >= 0 && reqVersion.Major == v22Version.Major {
			reqVersion = v22Version
		}
		version, err := ctrlcommon.IgnitionConverterSingleton().GetSupportedMinorVersion(*reqVersion)
		if err != nil {
			return nil, fmt.Errorf("unsupported Ignition version in Accept header: %s", acceptHeader)
		}
		return &version, nil
	}

	// default to serving spec v2.2 for all non-Ignition headers
	// as well as Ignition headers without a version specified.
	return v22Version, nil
}

// ServeHTTP handles /healthz requests.
func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "0")
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
	return
}

// defaultHandler is the HTTP Handler for backstopping invalid requests.
type defaultHandler struct{}

// ServeHTTP handles invalid requests.
func (h *defaultHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Length", "0")
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
	return
}

// getHTTPServerCfg returns the basic HTTP Server
func getHTTPServerCfg(addr string, handler http.Handler, tlsConfig *tls.Config) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: handler,
		// CVE-2016-2183: disable http/2, which by definition, requires insecure ciphers
		// Per https://golang.org/src/net/http/doc.go this is the runtime method of disabling HTTP/2
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		// We don't want to allow 1.1 as that's old.  This was flagged in a security audit.
		TLSConfig: tlsConfig,
	}

}

// Disable insecure cipher suites for CVE-2016-2183
// cipherOrder returns an ordered list of Ciphers that are considered secure
// Deprecated ciphers are not returned.
func cipherOrder() []uint16 {
	var first []uint16
	var second []uint16

	allowable := func(c *tls.CipherSuite) bool {
		// Disallow block ciphers using straight SHA1
		// See: https://tools.ietf.org/html/rfc7540#appendix-A
		if strings.HasSuffix(c.Name, "CBC_SHA") {
			return false
		}
		// 3DES is considered insecure
		if strings.Contains(c.Name, "3DES") {
			return false
		}
		return true
	}

	for _, c := range tls.CipherSuites() {
		for _, v := range c.SupportedVersions {
			if v == tls.VersionTLS13 {
				first = append(first, c.ID)
			}
			if v == tls.VersionTLS12 && allowable(c) {
				inFirst := false
				for _, id := range first {
					if c.ID == id {
						inFirst = true
						break
					}
				}
				if !inFirst {
					second = append(second, c.ID)
				}
			}
		}
	}

	return append(first, second...)
}
