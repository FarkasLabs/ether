package safefetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestReviewTLSSharedIP(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer s.Close()
	cert := s.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	// The httptest certificate is valid for example.com but not attacker.test.
	g := &Guard{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, s.Listener.Addr().String())
	}}
	c := g.Client(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}})
	defer c.CloseIdleConnections()
	t.Logf("certificate validates example.com=%v attacker.test=%v", cert.VerifyHostname("example.com"), cert.VerifyHostname("attacker.test"))
	for _, host := range []string{"example.com", "attacker.test"} {
		resp, err := c.Get("https://" + host + "/")
		if err != nil {
			if host == "attacker.test" {
				var mismatch x509.HostnameError
				if !errors.As(err, &mismatch) {
					t.Fatalf("expected hostname verification failure: %v", err)
				}
				return
			}
			t.Fatalf("%s: %v", host, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		t.Logf("%s TLS servername=%s", host, resp.TLS.ServerName)
		if host == "attacker.test" {
			t.Error("request succeeded despite certificate not valid for attacker.test")
		}
	}
}

func TestReviewRedirectRevalidatesAndPins(t *testing.T) {
	var dials []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		io.WriteString(w, r.Host)
	}))
	defer s.Close()
	g := &Guard{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials = append(dials, addr)
		var d net.Dialer
		return d.DialContext(ctx, network, s.Listener.Addr().String())
	}}
	c := g.Client(&http.Client{Transport: &http.Transport{}})
	defer c.CloseIdleConnections()
	r, e := c.Get("http://example.com/start")
	if e != nil {
		t.Fatal(e)
	}
	if r.Request.URL.String() != "http://example.com/end" {
		t.Errorf("response request URL = %s", r.Request.URL)
	}
	b, e := io.ReadAll(r.Body)
	r.Body.Close()
	if e != nil || string(b) != "example.com" {
		t.Fatalf("body %q error %v", b, e)
	}
	if len(dials) != 1 || dials[0] != "8.8.8.8:80" {
		t.Fatalf("dials %v", dials)
	}
}

func TestReviewProxyAuthAndInformationalResponse(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwQHNz" {
			t.Errorf("authorization %q", r.Header.Get("Proxy-Authorization"))
		}
		w.WriteHeader(103)
		io.WriteString(w, "ok")
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	u.User = url.UserPassword("user", "p@ss")
	g := &Guard{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}}
	c := g.Client(&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}})
	r, e := c.Post("http://example.com/", "text/plain", strings.NewReader("body"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(r.Body)
	r.Body.Close()
	if e != nil || r.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("status %d body %q error %v", r.StatusCode, b, e)
	}
}
func TestReviewProxyCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	g := &Guard{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}}
	c := g.Client(&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.com/", nil)
	done := make(chan error, 1)
	go func() { _, e := c.Do(req); done <- e }()
	<-entered
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Error("expected cancellation")
		}
	case <-time.After(time.Second):
		t.Error("cancellation did not unblock")
	}
	close(release)
}

func TestReviewDirectPoolSeparatesChangedIP(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer s.Close()
	var lookups int
	var dials []string
	g := &Guard{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials = append(dials, addr)
		var d net.Dialer
		return d.DialContext(ctx, network, s.Listener.Addr().String())
	}}
	c := g.Client(&http.Client{Transport: &http.Transport{}})
	defer c.CloseIdleConnections()
	for i := 0; i < 2; i++ {
		r, e := c.Get("http://example.com/")
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	if len(dials) != 2 || dials[0] != "8.8.8.8:80" || dials[1] != "1.1.1.1:80" {
		t.Fatalf("dials %v", dials)
	}
}

func TestReviewProxyResponseHeaderTimeout(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	g := &Guard{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}}
	c := g.Client(&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u), ResponseHeaderTimeout: 50 * time.Millisecond}})
	_, e := c.Get("http://example.com/")
	close(release)
	if e == nil {
		t.Fatal("expected response header timeout")
	}
}

func TestDirectFallsBackToValidatedAddress(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer s.Close()
	var calls []string
	g := &Guard{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}, nil
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls = append(calls, addr)
		if strings.HasPrefix(addr, "8.8.8.8:") {
			return nil, errors.New("unreachable")
		}
		if !strings.HasPrefix(addr, "1.1.1.1:") {
			return nil, errors.New("unvalidated address")
		}
		var d net.Dialer
		return d.DialContext(ctx, network, s.Listener.Addr().String())
	}}
	c := g.Client(&http.Client{Transport: &http.Transport{}})
	defer c.CloseIdleConnections()
	r, err := c.Get("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if len(calls) != 2 || calls[0] != "8.8.8.8:80" || calls[1] != "1.1.1.1:80" {
		t.Fatalf("dials: %v", calls)
	}
}

func TestFallbackBeforeSendingNonReplayableBody(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "payload" {
			t.Errorf("body = %q", b)
		}
		io.WriteString(w, "ok")
	}))
	defer s.Close()
	g := &Guard{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}, nil
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "8.8.8.8:") {
			return nil, errors.New("unreachable")
		}
		var d net.Dialer
		return d.DialContext(ctx, network, s.Listener.Addr().String())
	}}
	c := g.Client(&http.Client{Transport: &http.Transport{}})
	defer c.CloseIdleConnections()
	req, _ := http.NewRequest("POST", "http://example.com/", io.NopCloser(strings.NewReader("payload")))
	if req.GetBody != nil {
		t.Fatal("fixture must not be replayable")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestFallbackStopsOnDialCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	g := &Guard{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls++
		cancel()
		return nil, context.Canceled
	}}
	p := g.policyTransport(&http.Transport{}, false).(policyTransport)
	defer p.CloseIdleConnections()
	ips := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("1.1.1.1")}
	dec := &routeDecision{ips: ips, targetAddr: "8.8.8.8:80", poolAddr: "pool:80"}
	_, err := p.direct.DialContext(context.WithValue(ctx, routeDecisionKey{}, dec), "tcp", dec.poolAddr)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error %v, calls %d", err, calls)
	}
}
