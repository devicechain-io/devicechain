// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	listen := flag.String("listen", ":8080", "address to serve on")
	issuer := flag.String("issuer", "", "the issuer's certificate (PEM)")
	issuerKey := flag.String("issuer-key", "", "the issuer's private key (PEM)")
	wrongCert := flag.String("wrong-cert", "", "the certificate /wrongkey signs with (PEM), one the issuer did not issue")
	wrongKey := flag.String("wrong-key", "", "its private key (PEM)")
	status := flag.String("status", "", "the status file: a JSON object of lower-case hex serial -> good | revoked | unknown")
	logPath := flag.String("log", "", "append one JSON line per request to this file")
	flag.Parse()
	if err := run(*listen, *issuer, *issuerKey, *wrongCert, *wrongKey, *status, *logPath); err != nil {
		fmt.Fprintln(os.Stderr, "ocspresponder:", err)
		os.Exit(1)
	}
}

func run(listen, issuer, issuerKey, wrongCert, wrongKey, status, logPath string) error {
	if issuer == "" || issuerKey == "" || status == "" || logPath == "" {
		return fmt.Errorf("-issuer, -issuer-key, -status and -log are required")
	}
	r := &Responder{StatusFile: status}
	var err error
	if r.Issuer, err = LoadCert(issuer); err != nil {
		return err
	}
	if r.IssuerKey, err = LoadKey(issuerKey); err != nil {
		return err
	}
	if wrongCert != "" || wrongKey != "" {
		if r.WrongCert, err = LoadCert(wrongCert); err != nil {
			return err
		}
		if r.WrongKey, err = LoadKey(wrongKey); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	r.Log = f
	fmt.Printf("ocspresponder: serving %q on %s (/ok, /wrongkey, /stale)\n", r.Issuer.Subject.String(), listen)
	srv := &http.Server{Addr: listen, Handler: r, ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}
