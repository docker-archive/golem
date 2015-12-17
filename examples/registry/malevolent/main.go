package main

import (
	"flag"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution/registry/api/v2"
	"github.com/docker/libtrust"
	"github.com/gorilla/mux"
)

var listenAddr string
var registryAddr string
var notaryAddr string
var cert string
var certKey string
var key libtrust.PrivateKey

func init() {
	flag.StringVar(&listenAddr, "l", "localhost:6000", "Address to listen on")
	flag.StringVar(&registryAddr, "r", "http://localhost:5000", "Upstream registry to connect to")
	flag.StringVar(&notaryAddr, "n", "http://localhost:4443", "Upstream notary server to connect to")
	flag.StringVar(&cert, "c", "", "TLS certificate")
	flag.StringVar(&certKey, "k", "", "TLS certificate key")

	var err error
	key, err = libtrust.GenerateECP256PrivateKey()
	if err != nil {
		logrus.Fatalf("Error generating key: %s", err)
	}
}

func main() {
	flag.Parse()

	r, err := url.Parse(registryAddr)
	if err != nil {
		logrus.Fatalf("Error parsing registry address: %s", err)
	}
	n, err := url.Parse(notaryAddr)
	if err != nil {
		logrus.Fatalf("Error parsing notary address: %s", err)
	}

	rHandler := hostProxy(r)
	nHandler := hostProxy(n)

	router := v2.RouterWithPrefix("")
	router.GetRoute(v2.RouteNameBase).Handler(rHandler)

	// Configure notary routes
	router.Methods("POST").Path("/v2/{imageName:.*}/_trust/tuf/").Handler(nHandler)
	router.Methods("GET").Path("/v2/{imageName:.*}/_trust/tuf/{tufRole:(root|targets|snapshot)}.json").Handler(nHandler)
	router.Methods("GET").Path("/v2/{imageName:.*}/_trust/tuf/timestamp.json").Handler(nHandler)
	router.Methods("GET").Path("/v2/{imageName:.*}/_trust/tuf/timestamp.key").Handler(nHandler)
	router.Methods("DELETE").Path("/v2/{imageName:.*}/_trust/tuf/").Handler(nHandler)

	// Configure registry routes
	router.GetRoute(v2.RouteNameManifest).Handler(manifestChanger{rHandler})
	router.GetRoute(v2.RouteNameTags).Handler(rHandler)
	router.GetRoute(v2.RouteNameBlob).Handler(blobChanger{rHandler})
	router.GetRoute(v2.RouteNameBlobUpload).Handler(rHandler)
	router.GetRoute(v2.RouteNameBlobUploadChunk).Handler(rHandler)

	if cert != "" && certKey != "" {
		http.ListenAndServeTLS(listenAddr, cert, certKey, logWrapper{router})
	} else {
		http.ListenAndServe(listenAddr, logWrapper{router})
	}
}

func hostProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		addr := req.RemoteAddr
		if i := strings.Index(addr, ":"); i > 0 {
			addr = addr[:i]
		}
		xff := req.Header.Get("X-Forwarded-For")
		if xff == "" {
			xff = addr
		} else {
			xff = xff + ", " + addr
		}
		proto := "http"
		if req.TLS != nil && req.TLS.HandshakeComplete {
			proto = "https"
		}
		director(req)
		req.Header.Set("X-Real-IP", addr)
		req.Header.Set("X-Forwarded-Proto", proto)
		req.Header.Set("X-Forwarded-For", xff)
	}
	return proxy
}

type logWrapper struct {
	http.Handler
}

func (l logWrapper) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	logrus.Infof("Called %s: %s", r.Method, r.URL.String())
	l.Handler.ServeHTTP(rw, r)
}

func extractOperation(r *http.Request) string {
	vars := mux.Vars(r)
	name := vars["name"]
	return path.Base(name)
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
