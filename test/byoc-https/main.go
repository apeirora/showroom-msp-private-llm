// Command byoc-https exercises the BYOC TLS listener without a cluster or model.
// Run with CGO_ENABLED=0 so this executable also runs in the fixture container.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const image = "docker.io/library/traefik:v3.7.16@" +
	"sha256:1336e85aaed293bd63d005086e0dd013b04078b26aaeb1c18b26a2f928af7e65"
const hostname = "instance.byoc.test"
const dummyToken = "fixture-only-not-a-real-key"

type check struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

type report struct {
	Time                  string  `json:"time"`
	Image                 string  `json:"image"`
	Checks                []check `json:"passed"`
	SymlinkReloadObserved bool    `json:"symlink_reload_observed"`
	Error                 string  `json:"error,omitempty"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		serve()
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "probe" {
		c, err := net.DialTimeout("tcp", os.Args[2], time.Second)
		if err != nil {
			os.Exit(1)
		}
		_ = c.Close()
		return
	}
	out := flag.String("report", "", "write non-secret JSON acceptance report")
	flag.Parse()
	r := report{Time: time.Now().UTC().Format(time.RFC3339), Image: image}
	if err := run(&r); err != nil {
		r.Error = err.Error()
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		panic(err)
	}
	if *out != "" {
		if err := os.WriteFile(*out, append(b, '\n'), 0600); err != nil {
			panic(err)
		}
	}
	fmt.Println(string(b))
	if r.Error != "" {
		os.Exit(1)
	}
}

func serve() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "healthy\n")
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+dummyToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 3; i++ {
				_, _ = fmt.Fprintf(w, "data: {\"chunk\":%d}\n\n", i)
				w.(http.Flusher).Flush()
				time.Sleep(400 * time.Millisecond)
			}
			return
		}
		_, _ = io.WriteString(w, "{\"object\":\"list\",\"data\":[]}")
	})
	s := &http.Server{Addr: "127.0.0.1:8000", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := s.ListenAndServe(); err != nil {
		panic(err)
	}
}

func docker(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return b, fmt.Errorf("docker %s: %w: %s", args[0], err, b)
	}
	return b, nil
}

type fixture struct {
	report     *report
	dir        string
	name       string
	executable string
	address    string
	ca         *x509.Certificate
	caKey      *ecdsa.PrivateKey
	pool       *x509.CertPool
	trusted    *http.Client
	first      string
	now        time.Time
}

func run(r *report) (result error) {
	f := &fixture{report: r, name: fmt.Sprintf("byoc-https-%d", os.Getpid()), now: time.Now()}
	defer func() { result = errors.Join(result, f.close()) }()
	steps := []func() error{f.start, f.testHTTP, f.testTLSRejection, f.testPrivateModelPort, f.testRotation}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func (f *fixture) pass(name, detail string) {
	f.report.Checks = append(f.report.Checks, check{name, detail})
}

func (f *fixture) close() error {
	if f.trusted != nil {
		f.trusted.CloseIdleConnections()
	}
	// Scope cleanup to this process's names; no Docker-wide prune or volume deletion.
	_, _ = docker("rm", "-f", f.name+"-model", f.name)
	_, _ = docker("network", "rm", f.name)
	if f.dir != "" {
		return os.RemoveAll(f.dir)
	}
	return nil
}

const dynamicConfig = `http:
  routers:
    model:
      entryPoints: [websecure]
      rule: "Host(` + "`%s`" + `)"
      service: model
      tls: {}
  services:
    model:
      loadBalancer:
        servers:
          - url: http://127.0.0.1:8000
        responseForwarding:
          flushInterval: -1ms
tls:
  options:
    default:
      minVersion: VersionTLS12
      sniStrict: true
  certificates:
    - certFile: /tls/tls.crt
      keyFile: /tls/tls.key
`

func (f *fixture) start() error {
	var err error
	f.dir, err = os.MkdirTemp("", "byoc-https-")
	if err != nil {
		return err
	}
	// All private keys exist only in this mode-0700 temporary directory.
	if _, err = docker("pull", image); err != nil {
		return err
	}
	if _, err = docker("network", "create", f.name); err != nil {
		return err
	}
	for _, sub := range []string{"config", "tls"} {
		if err = os.Mkdir(filepath.Join(f.dir, sub), 0700); err != nil {
			return err
		}
	}
	config := []byte(fmt.Sprintf(dynamicConfig, hostname))
	if err = os.WriteFile(filepath.Join(f.dir, "config", "dynamic.yaml"), config, 0600); err != nil {
		return err
	}
	f.ca, f.caKey, err = authority()
	if err != nil {
		return err
	}
	f.pool = x509.NewCertPool()
	f.pool.AddCert(f.ca)
	f.first, err = f.install("first", hostname, f.now.Add(-time.Hour), f.now.Add(24*time.Hour))
	if err != nil {
		return err
	}
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	args := []string{"run", "-d", "--name", f.name, "--network", f.name, "--user", user,
		"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"-p", "127.0.0.1::8443", "-v", filepath.Join(f.dir, "config") + ":/config:ro",
		"-v", filepath.Join(f.dir, "tls") + ":/tls:ro", image,
		"--entrypoints.websecure.address=:8443", "--providers.file.filename=/config/dynamic.yaml",
		"--providers.file.watch=true", "--api=false", "--log.level=ERROR"}
	if _, err = docker(args...); err != nil {
		return err
	}
	f.executable, err = os.Executable()
	if err != nil {
		return err
	}
	if _, err = docker("run", "-d", "--name", f.name+"-model", "--network", "container:"+f.name,
		"--user", user, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"-v", f.executable+":/model:ro", "--entrypoint", "/model", image, "serve"); err != nil {
		return err
	}
	if err = f.refreshAddress(); err != nil {
		return err
	}
	f.trusted = f.newClient(f.pool, hostname)
	if err = f.ready(); err != nil {
		return err
	}
	f.pass("trusted_https", "CA and hostname verified")
	return nil
}

func (f *fixture) install(label, host string, from, until time.Time) (string, error) {
	return installCertificate(f.dir, label, host, from, until, f.ca, f.caKey)
}

func (f *fixture) refreshAddress() error {
	ports, err := docker("port", f.name, "8443/tcp")
	if err != nil {
		return err
	}
	f.address = strings.TrimSpace(string(ports))
	if !strings.HasPrefix(f.address, "127.0.0.1:") {
		return fmt.Errorf("unexpected port binding %q", f.address)
	}
	return nil
}

func (f *fixture) restart() error {
	if _, err := docker("restart", f.name); err != nil {
		return err
	}
	// Docker may allocate a new host port/network namespace on restart.
	// A Kubernetes Pod rollout likewise restarts both containers together.
	if _, err := docker("restart", f.name+"-model"); err != nil {
		return err
	}
	f.trusted.CloseIdleConnections()
	return f.refreshAddress()
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func (f *fixture) newClient(roots *x509.CertPool, serverName string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, CheckRedirect: rejectRedirect,
		Transport: &http.Transport{Proxy: nil,
			TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, f.address)
			}}}
}

func (f *fixture) get(c *http.Client, path, token string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, "https://"+hostname+path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.Do(req)
}

func (f *fixture) ready() error {
	var lastErr error
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		res, err := f.get(f.trusted, "/health", "")
		lastErr = err
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	proxyLogs, _ := docker("logs", "--tail", "5", f.name)
	modelLogs, _ := docker("logs", "--tail", "5", f.name+"-model")
	return fmt.Errorf("verified HTTPS health never became ready: last=%v; proxy=%s; model=%s",
		lastErr, proxyLogs, modelLogs)
}

func (f *fixture) testHTTP() error {
	for _, tc := range []struct {
		token string
		code  int
		name  string
	}{
		{"", 401, "missing_key_rejected"}, {"wrong", 401, "wrong_key_rejected"},
		{dummyToken, 200, "valid_key_accepted"},
	} {
		res, err := f.get(f.trusted, "/v1/models", tc.token)
		if err != nil {
			return err
		}
		_ = res.Body.Close()
		if res.StatusCode != tc.code {
			return fmt.Errorf("%s: got %d", tc.name, res.StatusCode)
		}
		f.pass(tc.name, "")
	}
	req, err := http.NewRequest(http.MethodGet, "https://"+hostname+"/v1/models", nil)
	if err != nil {
		return err
	}
	req.Host = "other.byoc.test"
	req.Header.Set("Authorization", "Bearer "+dummyToken)
	res, err := f.trusted.Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("unexpected cross-host status: %d", res.StatusCode)
	}
	f.pass("cross_host_routing_rejected", "valid TLS SNI plus another HTTP Host cannot select the model")
	return f.testStreaming()
}

func (f *fixture) testStreaming() error {
	start := time.Now()
	res, err := f.get(f.trusted, "/v1/chat/completions", dummyToken)
	if err != nil {
		return err
	}
	firstChunk := make([]byte, len("data: {\"chunk\":0}\n\n"))
	_, err = io.ReadFull(res.Body, firstChunk)
	firstDelay := time.Since(start)
	rest, readErr := io.ReadAll(res.Body)
	_ = res.Body.Close()
	total := time.Since(start)
	if err != nil || readErr != nil || firstDelay > 700*time.Millisecond || total < 1100*time.Millisecond ||
		!strings.Contains(string(rest), "\"chunk\":2") {
		return fmt.Errorf("stream buffering/failure: first=%s total=%s read=%v/%v", firstDelay, total, err, readErr)
	}
	f.pass("streaming", fmt.Sprintf("first chunk %dms; complete %dms", firstDelay.Milliseconds(), total.Milliseconds()))
	return nil
}

func (f *fixture) testTLSRejection() error {
	plain := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: rejectRedirect}
	defer plain.CloseIdleConnections()
	res, err := plain.Get("http://" + f.address + "/v1/models")
	if err == nil {
		_ = res.Body.Close()
		if res.StatusCode >= 200 && res.StatusCode < 400 {
			return fmt.Errorf("plaintext accepted: %d", res.StatusCode)
		}
	}
	f.pass("plaintext_rejected", "no redirect or successful model response")
	for _, tc := range []struct {
		name, server string
		roots        *x509.CertPool
	}{
		{"untrusted_cert_rejected", hostname, x509.NewCertPool()}, {"unknown_sni_rejected", "other.byoc.test", f.pool},
	} {
		c := f.newClient(tc.roots, tc.server)
		res, err = f.get(c, "/health", "")
		c.CloseIdleConnections()
		if err == nil {
			_ = res.Body.Close()
			return fmt.Errorf("%s unexpectedly succeeded", tc.name)
		}
		f.pass(tc.name, "")
	}
	// Intentionally disable client verification only to test server rejection without SNI.
	conn, err := tls.Dial("tcp", f.address, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err == nil {
		_ = conn.Close()
		return errors.New("missing SNI unexpectedly accepted")
	}
	f.pass("missing_sni_rejected", "")
	return nil
}

func (f *fixture) testPrivateModelPort() error {
	ipBytes, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", f.name)
	if err != nil {
		return err
	}
	ip := strings.TrimSpace(string(ipBytes))
	probe := func(port string) error {
		_, e := docker("run", "--rm", "--network", f.name, "-v", f.executable+":/model:ro",
			"--entrypoint", "/model", image, "probe", ip+":"+port)
		return e
	}
	if err = probe("8443"); err != nil {
		return fmt.Errorf("probe cannot reach TLS listener: %w", err)
	}
	if err = probe("8000"); err == nil {
		return errors.New("model port reachable outside shared network namespace")
	}
	f.pass("model_port_private", "sibling container reaches 8443 but cannot reach 8000")
	return nil
}

func (f *fixture) fingerprint() (string, error) {
	c, err := tls.Dial("tcp", f.address, &tls.Config{RootCAs: f.pool, ServerName: hostname, MinVersion: tls.VersionTLS12})
	if err != nil {
		return "", err
	}
	defer func() { _ = c.Close() }()
	h := sha256.Sum256(c.ConnectionState().PeerCertificates[0].Raw)
	return hex.EncodeToString(h[:]), nil
}

func (f *fixture) testRotation() error {
	observed, err := f.fingerprint()
	if err != nil || observed != f.first {
		return fmt.Errorf("first certificate mismatch: %v", err)
	}
	second, err := f.install("second", hostname, f.now.Add(-time.Hour), f.now.Add(48*time.Hour))
	if err != nil {
		return err
	}
	// Observe kubelet-style ..data replacement; do not rely on file watcher reload.
	for end := time.Now().Add(4 * time.Second); time.Now().Before(end); {
		observed, _ = f.fingerprint()
		if observed == second {
			f.report.SymlinkReloadObserved = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err = f.restart(); err != nil {
		return err
	}
	if err = f.ready(); err != nil {
		return err
	}
	observed, err = f.fingerprint()
	if err != nil || observed != second || second == f.first {
		return fmt.Errorf("replacement certificate not served after restart: %v", err)
	}
	f.pass("certificate_replacement", "atomic ..data replacement and restart serve the new verified fingerprint")
	return f.testInvalidCertificates()
}

func (f *fixture) testInvalidCertificates() error {
	for _, tc := range []struct {
		name, host  string
		from, until time.Time
	}{
		{"expired_certificate_rejected", hostname, f.now.Add(-48 * time.Hour), f.now.Add(-time.Hour)},
		{"wrong_hostname_certificate_rejected", "wrong.byoc.test", f.now.Add(-time.Hour), f.now.Add(time.Hour)},
	} {
		if _, err := f.install(tc.name, tc.host, tc.from, tc.until); err != nil {
			return err
		}
		if err := f.restart(); err != nil {
			return err
		}
		if err := f.requireInvalidTLS(); err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
		f.pass(tc.name, "")
	}
	return nil
}

func (f *fixture) requireInvalidTLS() error {
	// Restart returns before initialization; require an actual TLS rejection, not a closed port.
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		cfg := &tls.Config{RootCAs: f.pool, ServerName: hostname, MinVersion: tls.VersionTLS12}
		c, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", f.address, cfg)
		if err == nil {
			_ = c.Close()
			return errors.New("invalid certificate unexpectedly accepted")
		}
		var verify *tls.CertificateVerificationError
		if errors.As(err, &verify) || strings.Contains(err.Error(), "unrecognized name") {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("no discriminating TLS failure")
}

func authority() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1),
		Subject:   pkix.Name{CommonName: "Throwaway BYOC fixture CA"},
		NotBefore: now.Add(-72 * time.Hour), NotAfter: now.Add(72 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	ca, err = x509.ParseCertificate(der)
	return ca, key, err
}

func installCertificate(
	dir, label, host string, from, until time.Time, ca *x509.Certificate, caKey *ecdsa.PrivateKey,
) (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return "", err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host},
		DNSNames: []string{host}, NotBefore: from, NotAfter: until,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "tls", label)
	if err = os.Mkdir(dest, 0700); err != nil {
		return "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err = os.WriteFile(filepath.Join(dest, "tls.crt"), certPEM, 0600); err != nil {
		return "", err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err = os.WriteFile(filepath.Join(dest, "tls.key"), keyPEM, 0600); err != nil {
		return "", err
	}
	for _, file := range []string{"tls.crt", "tls.key"} {
		path := filepath.Join(dir, "tls", file)
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			if err = os.Symlink("..data/"+file, path); err != nil {
				return "", err
			}
		}
	}
	if err = os.Symlink(label, filepath.Join(dir, "tls", "..data-new")); err != nil {
		return "", err
	}
	if err = os.Rename(filepath.Join(dir, "tls", "..data-new"), filepath.Join(dir, "tls", "..data")); err != nil {
		return "", err
	}
	hash := sha256.Sum256(der)
	return hex.EncodeToString(hash[:]), nil
}
