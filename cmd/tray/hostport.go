package main

import (
	"fmt"
	"net"
)

// resolveHostPort derives the host/port pair the tray polls /healthcheck
// against and opens a browser to, from HTTP_ADDR (e.g. ":8080",
// "0.0.0.0:8080", "127.0.0.1:9090"). An empty or wildcard host — as bound
// by ":8080"'s implicit empty host, "0.0.0.0", or "::" — tells net.Listen
// "listen on every interface", but is meaningless to browse to or poll
// directly, so "localhost" is substituted: a human or an HTTP client on
// the same machine reaches the server that way regardless of which
// interfaces it actually bound to.
func resolveHostPort(httpAddr string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(httpAddr)
	if err != nil {
		return "", "", fmt.Errorf("parsing HTTP_ADDR %q: %w", httpAddr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return host, port, nil
}

// baseURL derives the "http://host:port" root the tray polls/opens, from
// HTTP_ADDR. See resolveHostPort for the host-substitution rule.
func baseURL(httpAddr string) (string, error) {
	host, port, err := resolveHostPort(httpAddr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s:%s", host, port), nil
}
