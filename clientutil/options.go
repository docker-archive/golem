package clientutil

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	defaultDockerSocket       = "unix:///var/run/docker.sock"
	defaultCertDir            = "$HOME/.docker"
	defaultCACertFilename     = "ca.pem"
	defaultClientCertFilename = "cert.pem"
	defaultClientKeyFilename  = "key.pem"
)

type ClientOptions struct {
	parseL    sync.Mutex
	parsed    bool
	tlsConfig *tls.Config

	// flags
	daemonURL      string
	useTLS         bool
	verifyTLS      bool
	caCertFile     string
	clientCertFile string
	clientKeyFile  string
}

func NewClientOptions() *ClientOptions {
	co := &ClientOptions{}
	flag.StringVar(&co.daemonURL, "H", "", "Docker daemon socket/host to connect to")
	flag.BoolVar(&co.useTLS, "-tls", false, "Use TLS client cert/key (implied by --tlsverify)")
	flag.BoolVar(&co.verifyTLS, "-tlsverify", true, "Use TLS and verify the remote server certificate")
	flag.StringVar(&co.caCertFile, "-cacert", "", "Trust certs signed only by this CA")
	flag.StringVar(&co.clientCertFile, "-cert", "", "TLS client certificate")
	flag.StringVar(&co.clientKeyFile, "-key", "", "TLS client key")

	return co
}

func (co *ClientOptions) parse() {
	co.parseL.Lock()
	defer co.parseL.Unlock()
	if co.parsed {
		return
	}
	if !flag.Parsed() {
		panic("flags must be parsed before accessing data")
	}

	// Command line option takes preference, then fallback to environment var,
	// then fallback to default.
	if co.daemonURL == "" {
		if co.daemonURL = os.Getenv("DOCKER_HOST"); co.daemonURL == "" {
			co.daemonURL = defaultDockerSocket
		}
	}

	// Setup TLS config.
	if co.useTLS || co.verifyTLS || os.Getenv("DOCKER_TLS_VERIFY") != "" {
		co.tlsConfig = &tls.Config{
			InsecureSkipVerify: !co.verifyTLS,
		}

		// Get the cert path specified by environment variable or default.
		certDir := os.Getenv("DOCKER_CERT_PATH")
		if certDir == "" {
			certDir = defaultCertDir
		}
		certDir = os.ExpandEnv(certDir)

		// Get CA cert bundle.
		if co.caCertFile == "" { // Not set on command line.
			co.caCertFile = filepath.Join(certDir, defaultCACertFilename)
			if _, err := os.Stat(co.caCertFile); os.IsNotExist(err) {
				// CA cert bundle does not exist in default location.
				// We'll use the system default root CAs instead.
				co.caCertFile = ""
			}
		}

		if co.caCertFile != "" {
			certBytes, err := ioutil.ReadFile(co.caCertFile)
			if err != nil {
				log.Fatalf("unable to read ca cert file: %s", err)
			}

			co.tlsConfig.RootCAs = x509.NewCertPool()
			if !co.tlsConfig.RootCAs.AppendCertsFromPEM(certBytes) {
				log.Fatal("unable to load ca cert file")
			}
		}

		// Get client cert.
		if co.clientCertFile == "" { // Not set on command line.
			co.clientCertFile = filepath.Join(certDir, defaultClientCertFilename)
			if _, err := os.Stat(co.clientCertFile); os.IsNotExist(err) {
				// Client cert does not exist in default location.
				co.clientCertFile = ""
			}
		}

		// Get client key.
		if co.clientKeyFile == "" { // Not set on commadn line.
			co.clientKeyFile = filepath.Join(certDir, defaultClientKeyFilename)
			if _, err := os.Stat(co.clientKeyFile); os.IsNotExist(err) {
				// Client key does not exist in default location.
				co.clientKeyFile = ""
			}
		}

		// If one of client cert/key is specified then both must be.
		certSpecified := co.clientCertFile != ""
		keySpecified := co.clientKeyFile != ""
		if certSpecified != keySpecified {
			log.Fatal("must specify both client certificate and key")
		}

		// If both are specified, load them into the tls config.
		if certSpecified && keySpecified {
			tlsClientCert, err := tls.LoadX509KeyPair(co.clientCertFile, co.clientKeyFile)
			if err != nil {
				log.Fatalf("unable to load client cert/key pair: %s", err)
			}

			co.tlsConfig.Certificates = append(co.tlsConfig.Certificates, tlsClientCert)
		}
	}
}

func (co *ClientOptions) DaemonURL() string {
	co.parse()
	return co.daemonURL
}

func (co *ClientOptions) TLSConfig() *tls.Config {
	co.parse()
	return co.tlsConfig
}
