package driver

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mehrabr/duckcall"
)

// ParseDSN turns a duckcall DSN into a Config.
//
//	quack://analytics.internal:8888?token=env:QUACK_TOKEN
//
// Schemes: quack (plain HTTP, the protocol's default) and quacks (TLS).
// token is either a literal or env:NAME to read from the environment at
// open time — the env form keeps secrets out of connection strings, which
// tend to end up in logs and process listings.
//
// Other params: fetch_workers, connect_timeout (Go duration), max_retries.
func ParseDSN(dsn string) (duckcall.Config, error) {
	var cfg duckcall.Config
	u, err := url.Parse(dsn)
	if err != nil {
		return cfg, fmt.Errorf("duckcall: unparseable DSN")
	}
	var scheme string
	switch u.Scheme {
	case "quack", "http":
		scheme = "http"
	case "quacks", "https":
		scheme = "https"
	default:
		return cfg, fmt.Errorf("duckcall: DSN scheme %q (want quack:// or quacks://)", u.Scheme)
	}
	if u.Host == "" {
		return cfg, fmt.Errorf("duckcall: DSN missing host")
	}
	if u.User != nil {
		return cfg, fmt.Errorf("duckcall: quack has no user model; put the token in ?token=")
	}
	cfg.Endpoint = scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/")

	for key, vals := range u.Query() {
		v := vals[len(vals)-1]
		switch key {
		case "token":
			if name, ok := strings.CutPrefix(v, "env:"); ok {
				v = os.Getenv(name)
				if v == "" {
					return cfg, fmt.Errorf("duckcall: token env %s is empty or unset", name)
				}
			}
			cfg.Token = v
		case "fetch_workers":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return cfg, fmt.Errorf("duckcall: bad fetch_workers %q", v)
			}
			cfg.FetchWorkers = n
		case "connect_timeout":
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return cfg, fmt.Errorf("duckcall: bad connect_timeout %q", v)
			}
			cfg.ConnectTimeout = d
		case "max_retries":
			n, err := strconv.Atoi(v)
			if err != nil {
				return cfg, fmt.Errorf("duckcall: bad max_retries %q", v)
			}
			cfg.MaxRetries = n
		default:
			return cfg, fmt.Errorf("duckcall: unknown DSN param %q", key)
		}
	}
	return cfg, nil
}
