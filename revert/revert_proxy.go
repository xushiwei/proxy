// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// HTTP reverse proxy handler

package revert

import (
	"bytes"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ReverseProxy is an HTTP Handler that takes an incoming request and
// sends it to another server, proxying the response back to the
// client.
type ReverseProxy struct {
	// Director must be a function which modifies
	// the request into a new request to be sent
	// using Transport. Its response is then copied
	// back to the original client unmodified.
	Director func(*http.Request)

	// The transport used to perform proxy requests.
	// If nil, http.DefaultTransport is used.
	Transport http.RoundTripper

	// FlushInterval specifies the flush interval, in
	// nanoseconds, to flush to the client while
	// coping the response body.
	// If zero, no periodic flushing is done.
	FlushInterval int64

	DomainProxy, Domain string
	forbiddens          []string
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// NewSingleHostReverseProxy returns a new ReverseProxy that rewrites
// URLs to the scheme, host, and base path provided in target. If the
// target's path is "/base" and the incoming request was for "/dir",
// the target request will be for /base/dir.
func newSingleHostReverseProxy(target *url.URL) *ReverseProxy {
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
		if q := req.URL.RawQuery; q != "" {
			req.URL.RawPath = req.URL.Path + "?" + q
		} else {
			req.URL.RawPath = req.URL.Path
		}
		req.URL.RawQuery = target.RawQuery
	}
	return &ReverseProxy{Director: director}
}

func New(domainProxy, domain string) (proxy *ReverseProxy, err error) {
	u, err := url.Parse("http://" + domainProxy)
	if err != nil {
		return
	}
	proxy = newSingleHostReverseProxy(u)
	proxy.DomainProxy, proxy.Domain = domainProxy, domain
	return
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func (p *ReverseProxy) Forbidden(prefix string) {
	p.forbiddens = append(p.forbiddens, prefix)
}

func (p *ReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	transport := p.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	outreq := new(http.Request)
	*outreq = *req // includes shallow copies of maps, but okay

	p.Director(outreq)
	outreq.Proto = "HTTP/1.1"
	outreq.ProtoMajor = 1
	outreq.ProtoMinor = 1
	outreq.Close = false

	// Remove the connection header to the backend.  We want a
	// persistent connection, regardless of what the client sent
	// to us.  This is modifying the same underlying map from req
	// (shallow copied above) so we only copy it if necessary.
	if outreq.Header.Get("Connection") != "" {
		outreq.Header = make(http.Header)
		copyHeader(outreq.Header, req.Header)
		outreq.Header.Del("Connection")
	}

	if clientIp, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		outreq.Header.Set("X-Forwarded-For", clientIp)
	}

	outreq.URL.Host = p.DomainProxy
	outreq.Host = outreq.URL.Host
	// spew.Dump(outreq)
	dmp, _ := httputil.DumpRequest(outreq, false)
	println("request 。。。。")
	log.Println(string(dmp))

	res, err := transport.RoundTrip(outreq)
	if err != nil {
		log.Printf("http: proxy error: %v", err)
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	copyHeader(rw.Header(), res.Header)

	dmp2, _ := httputil.DumpResponse(res, false)
	println("response。。。。")
	log.Println(string(dmp2))

	switch res.StatusCode {
	case 301, 302, 307:
		l := res.Header["Location"]
		if l != nil {
			loc := l[0]
			log.Println("Redirect location:", loc)
			for _, forbidden := range p.forbiddens {
				if strings.HasPrefix(loc, forbidden) {
					rw.WriteHeader(404)
					return
				}
			}
		}
	}

	rw.WriteHeader(res.StatusCode)
	if res.Body != nil {
		var dst io.Writer = rw
		if p.FlushInterval != 0 {
			if wf, ok := rw.(writeFlusher); ok {
				dst = &maxLatencyWriter{dst: wf, latency: p.FlushInterval}
			}
		}
		if res.StatusCode == 200 {
			ct := res.Header["Content-Type"]
			if ct != nil && ct[0] == "text/plain; charset=utf-8" {
				log.Println("Transform: text/plain; charset=utf-8")
				buf := bytes.NewBuffer(nil)
				io.Copy(buf, res.Body)
				b := bytes.Replace(buf.Bytes(), []byte(p.DomainProxy), []byte(p.Domain), -1)
				io.Copy(dst, bytes.NewBuffer(b))
				return
			}
		}
		io.Copy(dst, res.Body)
	}
}

type writeFlusher interface {
	io.Writer
	http.Flusher
}

type maxLatencyWriter struct {
	dst     writeFlusher
	latency int64 // nanos

	lk   sync.Mutex // protects init of done, as well Write + Flush
	done chan bool
}

func (m *maxLatencyWriter) Write(p []byte) (n int, err error) {
	m.lk.Lock()
	defer m.lk.Unlock()
	if m.done == nil {
		m.done = make(chan bool)
		go m.flushLoop()
	}
	n, err = m.dst.Write(p)
	if err != nil {
		m.done <- true
	}
	return
}

func (m *maxLatencyWriter) flushLoop() {
	t := time.NewTicker(time.Duration(m.latency))
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.lk.Lock()
			m.dst.Flush()
			m.lk.Unlock()
		case <-m.done:
			return
		}
	}
	panic("unreached")
}
