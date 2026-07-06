// quackbouncer is a connection-pooling proxy for the Quack protocol: TLS
// termination and per-client tokens in front of a plain-HTTP quack_serve,
// with Prometheus metrics on /metrics. It is built on duckcall's wire layer
// alone and never decodes a result payload.
//
//	quackbouncer -listen :8443 -tls-cert cert.pem -tls-key key.pem \
//	    -upstream http://127.0.0.1:8888 -tokens tokens.conf
//
// The upstream token is read from $QUACKBOUNCER_UPSTREAM_TOKEN. The tokens
// file maps client tokens to names, one "name:token" per line, # comments.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/mehrabr/duckcall/wire"
)

func main() {
	var (
		listen   = flag.String("listen", ":8443", "address to listen on")
		upstream = flag.String("upstream", "", "quack_serve base URL, e.g. http://127.0.0.1:8888")
		tokens   = flag.String("tokens", "", "path to client tokens file (name:token per line)")
		poolSize = flag.Int("pool", 8, "max idle upstream sessions")
		tlsCert  = flag.String("tls-cert", "", "TLS certificate (PEM)")
		tlsKey   = flag.String("tls-key", "", "TLS key (PEM)")
	)
	flag.Parse()
	if *upstream == "" || *tokens == "" {
		flag.Usage()
		os.Exit(2)
	}
	upstreamToken := os.Getenv("QUACKBOUNCER_UPSTREAM_TOKEN")
	if upstreamToken == "" {
		log.Fatal("QUACKBOUNCER_UPSTREAM_TOKEN is unset")
	}
	tokenMap, err := loadTokens(*tokens)
	if err != nil {
		log.Fatal(err)
	}

	m := newMetrics()
	pl := newPool(wire.Config{Endpoint: *upstream, Token: upstreamToken}, *poolSize)
	px := newProxy(tokenMap, pl, m)

	srv := &http.Server{Addr: *listen, Handler: px}
	if *tlsCert != "" && *tlsKey != "" {
		log.Printf("quackbouncer: listening on %s (TLS), upstream %s, %d client tokens", *listen, *upstream, len(tokenMap))
		log.Fatal(srv.ListenAndServeTLS(*tlsCert, *tlsKey))
	}
	// Serving without TLS forfeits the main reason to run a bouncer; allow
	// it for local work but say so every time.
	log.Printf("quackbouncer: WARNING: serving plain HTTP on %s — client tokens travel unencrypted", *listen)
	log.Fatal(srv.ListenAndServe())
}

func loadTokens(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tokens := map[string]string{}
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		name, token, ok := strings.Cut(text, ":")
		if !ok || name == "" || token == "" {
			return nil, fmt.Errorf("%s:%d: want name:token", path, line)
		}
		tokens[strings.TrimSpace(token)] = strings.TrimSpace(name)
	}
	return tokens, sc.Err()
}
