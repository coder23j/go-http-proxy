package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const loopbackHost = "127.0.0.1"

var (
	embeddedIPv4PortPattern = regexp.MustCompile(`\d+-\d+-\d+-\d+-\d+`)
	portPattern             = regexp.MustCompile(`\d+`)
)

type proxyServer struct {
	listenAddr string
	listenPort string
}

func main() {
	listen := flag.String("listen", "127.0.0.1:21842", "listen address, for example :8080 or 127.0.0.1:8080")
	flag.Parse()

	listenAddr, listenPort, err := normalizeListenAddress(*listen)
	if err != nil {
		log.Fatalf("invalid listen address: %v", err)
	}

	server := &http.Server{
		Addr: listenAddr,
		Handler: proxyServer{
			listenAddr: listenAddr,
			listenPort: listenPort,
		},
	}

	log.Printf("listening on %s", listenAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (server proxyServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	target, err := targetFromHost(request.Host)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	if server.isSelfForward(target) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte("禁止自转发\n"))
		return
	}

	targetURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(target.Host, target.Port)}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		http.Error(writer, "proxy error: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(writer, request)
}

func (server proxyServer) isSelfForward(target targetAddress) bool {
	if target.Port != server.listenPort {
		return false
	}

	listenHost, _, err := net.SplitHostPort(server.listenAddr)
	if err != nil {
		return false
	}

	return isLoopbackAddress(target.Host) && (listenHost == "" || listenHost == "0.0.0.0" || listenHost == "::" || isLoopbackAddress(listenHost))
}

type targetAddress struct {
	Host string
	Port string
}

func targetFromHost(hostHeader string) (targetAddress, error) {
	host, err := hostWithoutPort(hostHeader)
	if err != nil {
		return targetAddress{}, err
	}

	prefix := strings.Split(host, ".")[0]
	return parsePrefix(prefix)
}

func parsePrefix(prefix string) (targetAddress, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return targetAddress{}, errors.New("host prefix is empty")
	}

	if target, ok, err := firstEmbeddedIPv4Port(prefix); ok || err != nil {
		return target, err
	}

	port, ok := firstValidPort(prefix)
	if !ok {
		return targetAddress{}, fmt.Errorf("host prefix %q must contain a valid port or numeric-ip-port", prefix)
	}

	return targetAddress{Host: loopbackHost, Port: port}, nil
}

func hostWithoutPort(hostHeader string) (string, error) {
	hostHeader = strings.TrimSpace(hostHeader)
	if hostHeader == "" {
		return "", errors.New("host header is empty")
	}

	if strings.HasPrefix(hostHeader, "[") {
		host, _, err := net.SplitHostPort(hostHeader)
		if err != nil {
			return "", fmt.Errorf("invalid bracketed host: %w", err)
		}
		return strings.Trim(host, "[]"), nil
	}

	if host, _, err := net.SplitHostPort(hostHeader); err == nil {
		return host, nil
	}

	return hostHeader, nil
}

func isValidPort(value string) bool {
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535
}

func firstEmbeddedIPv4Port(value string) (targetAddress, bool, error) {
	matches := embeddedIPv4PortPattern.FindAllString(value, -1)
	for _, match := range matches {
		parts := strings.Split(match, "-")
		octets := parts[:4]
		port := parts[4]
		if !isValidPort(port) {
			continue
		}
		if !isIPv4Octets(octets) {
			return targetAddress{}, true, fmt.Errorf("host prefix %q contains an invalid numeric IP", value)
		}
		return targetAddress{Host: strings.Join(octets, "."), Port: port}, true, nil
	}
	return targetAddress{}, false, nil
}

func firstValidPort(value string) (string, bool) {
	matches := portPattern.FindAllString(value, -1)
	for _, match := range matches {
		if isValidPort(match) {
			return match, true
		}
	}
	return "", false
}

func isIPv4Octets(values []string) bool {
	if len(values) != 4 {
		return false
	}
	for _, value := range values {
		if value == "" {
			return false
		}
		octet, err := strconv.Atoi(value)
		if err != nil || octet < 0 || octet > 255 {
			return false
		}
	}
	return true
}

func isLoopbackAddress(value string) bool {
	value = strings.Trim(value, "[]")
	if value == "localhost" {
		return true
	}
	ip := net.ParseIP(value)
	return ip != nil && ip.IsLoopback()
}

func init() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

func normalizeListenAddress(listen string) (string, string, error) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "", "", errors.New("listen address is empty")
	}

	if strings.HasPrefix(listen, ":") {
		listen = "0.0.0.0" + listen
	}

	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", "", err
	}
	if !isValidPort(port) {
		return "", "", fmt.Errorf("invalid listen port %q", port)
	}

	return net.JoinHostPort(host, port), port, nil
}
