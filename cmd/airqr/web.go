package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed web/*
var scannerAssets embed.FS

func runWeb(args []string) error {
	fsFlags := flag.NewFlagSet("web", flag.ExitOnError)
	var addr string
	var certFile string
	var keyFile string
	fsFlags.StringVar(&addr, "addr", "127.0.0.1:8747", "address to serve the scanner on")
	fsFlags.StringVar(&certFile, "tls-cert", "", "TLS certificate file for HTTPS")
	fsFlags.StringVar(&keyFile, "tls-key", "", "TLS private key file for HTTPS")
	if err := fsFlags.Parse(args); err != nil {
		return err
	}
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be provided together")
	}

	staticFiles, err := fs.Sub(scannerAssets, "web")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/", noCache(http.FileServer(http.FS(staticFiles))))

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	scheme := "http"
	if certFile != "" {
		scheme = "https"
	}
	printListenURLs(scheme, addr)
	fmt.Println("Camera access works on localhost or HTTPS secure contexts.")
	if certFile != "" {
		return server.ListenAndServeTLS(certFile, keyFile)
	}
	return server.ListenAndServe()
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func printListenURLs(scheme string, addr string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Printf("AirQR scanner: %s://%s\n", scheme, addr)
		return
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		fmt.Printf("AirQR scanner: %s://127.0.0.1:%s\n", scheme, port)
		for _, addr := range localIPv4s() {
			fmt.Printf("Phone URL (%s): %s://%s:%s\n", addr.Interface, scheme, addr.IP, port)
		}
		return
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	fmt.Printf("AirQR scanner: %s://%s:%s\n", scheme, host, port)
}

type localAddr struct {
	Interface string
	IP        string
}

func localIPv4s() []localAddr {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []localAddr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			ip = ip.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			ips = append(ips, localAddr{Interface: iface.Name, IP: ip.String()})
		}
	}
	return ips
}

func isVirtualInterface(name string) bool {
	for _, prefix := range []string{"br-", "docker", "veth", "virbr", "vmnet"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
