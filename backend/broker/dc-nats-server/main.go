// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// main and usageStr follow nats-server's main.go at v2.15.1, Copyright 2012-2026 The NATS Authors,
// Apache License 2.0, changed only where the comments below say so.

// Command nats-server is DeviceChain's broker: upstream nats-server's own main, built on the
// unmodified upstream library, plus one setting its configuration file cannot express.
//
// The MQTT listener requests (but does not require) a client certificate, so a device that
// authenticates by certificate proves possession of its key in the TLS handshake and the auth
// callout receives its chain, while every device that authenticates by token or password is
// unaffected. See applyDeviceTLS for the mechanism and why it must be re-applied on every reload.
//
// 🔴 THE BINARY IS NAMED nats-server, NOT dc-nats-server, AND THAT IS A CONTRACT. The nats Helm
// chart's preStop hook runs `nats-server -sl=ldm=/var/run/nats/nats.pid` in the server container
// to start a lame duck shutdown; ko names a binary after the last element of its import path, so
// this module's path ends in /nats-server and the hook finds it on PATH, unchanged. The image is
// named after the directory (dc-nats-server), like every other image the release publishes.
//
// Everything on the command line means what it means to upstream: flags and the config file are
// parsed by the library's own ConfigureOptions, and `-sl` (signal a running server) works through
// it unchanged. This binary adds no flag. Its one setting of its own, the metrics listen address,
// is the DC_BROKER_METRICS_ADDR environment variable.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/nats-io/nats-server/v2/server"
)

var usageStr = `
Usage: nats-server [options]

Server Options:
    -a, --addr, --net <host>         Bind to host address (default: 0.0.0.0)
    -p, --port <port>                Use port for clients (default: 4222)
    -n, --name
        --server_name <server_name>  Server name (default: auto)
    -P, --pid <file>                 File to store PID
    -m, --http_port <port>           Use port for http monitoring
    -ms,--https_port <port>          Use port for https monitoring
    -c, --config <file>              Configuration file
    -t                               Test configuration and exit
    -sl,--signal <signal>[=<pid>]    Send signal to nats-server process (ldm, stop, quit, term, reopen, reload)
                                     <pid> can be either a PID (e.g. 1) or the path to a PID file (e.g. /var/run/nats-server.pid)
        --client_advertise <string>  Client URL to advertise to other servers
        --ports_file_dir <dir>       Creates a ports file in the specified directory (<executable_name>_<pid>.ports).

Logging Options:
    -l, --log <file>                 File to redirect log output
    -T, --logtime                    Timestamp log entries (default: true)
        --logtime_utc                Timestamps in UTC instead of local timezone (default: false)
    -s, --syslog                     Log to syslog or windows event log
    -r, --remote_syslog <addr>       Syslog server addr (udp://localhost:514)
    -D, --debug                      Enable debugging output
    -V, --trace                      Trace the raw protocol
    -VV                              Verbose trace (traces system account as well)
    -DV                              Debug and trace
    -DVV                             Debug and verbose trace (traces system account as well)
        --log_size_limit <limit>     Logfile size limit (default: auto)
        --max_traced_msg_len <len>   Maximum printable length for traced messages (default: unlimited)

JetStream Options:
    -js, --jetstream                 Enable JetStream functionality
    -sd, --store_dir <dir>           Set the storage directory

Authorization Options:
        --user <user>                User required for connections
        --pass <password>            Password required for connections
        --auth <token>               Authorization token required for connections

TLS Options:
        --tls                        Enable TLS, do not verify clients (default: false)
        --tlscert <file>             Server certificate file
        --tlskey <file>              Private key for server certificate
        --tlsverify                  Enable TLS, verify client certificates
        --tlscacert <file>           Client certificate CA for verification

Cluster Options:
        --routes <rurl-1, rurl-2>    Routes to solicit and connect
        --cluster <cluster-url>      Cluster URL for solicited routes
        --cluster_name <string>      Cluster Name, if not set one will be dynamically generated
        --no_advertise <bool>        Do not advertise known cluster information to clients
        --cluster_advertise <string> Cluster URL to advertise to other servers
        --connect_retries <number>   For implicit routes, number of connect retries
        --cluster_listen <url>       Cluster url from which members can solicit routes

Profiling Options:
        --profile <port>             Profiling HTTP port

Common Options:
    -h, --help                       Show this message
    -v, --version                    Show version
        --help_tls                   TLS help

DeviceChain (environment):
    DC_BROKER_METRICS_ADDR           Listen address of the broker's own /metrics (default: :7778)
`

// usage will print out the flag options for the server.
func usage() {
	fmt.Printf("%s\n", usageStr)
	os.Exit(0)
}

func main() {
	exe := "nats-server"

	// Refuse a platform whose reload path this binary does not own (signals_windows.go), before
	// anything starts.
	if err := platformSupported(); err != nil {
		server.PrintAndDie(fmt.Sprintf("%s: %s", exe, err))
	}

	// Create a FlagSet and sets the usage
	fs := flag.NewFlagSet(exe, flag.ExitOnError)
	fs.Usage = usage

	// Configure the options from the flags/config file
	opts, err := server.ConfigureOptions(fs, os.Args[1:],
		server.PrintServerAndExit,
		fs.Usage,
		server.PrintTLSHelpAndDie)
	if err != nil {
		server.PrintAndDie(fmt.Sprintf("%s: %s", exe, err))
	} else if opts.CheckConfig {
		fmt.Fprintf(os.Stderr, "%s: configuration file %s is valid (%s)\n", exe, opts.ConfigFile, opts.ConfigDigest())
		os.Exit(0)
	}

	// Redact secret arguments before expvar reads them.
	server.RedactArgs(os.Args)

	// DeviceChain: request (never require) a client certificate on the MQTT listener, and own the
	// signals so that a reload keeps doing so. Subscribing before the server exists leaves no
	// window in which a SIGHUP would take its default action and terminate the process.
	prepareOptions(opts)
	sigs := notifySignals()

	// DeviceChain: bind the metrics endpoint before starting, so a port that cannot be had stops
	// the broker at once with a reason rather than leaving it running unwatched.
	metricsAddr := os.Getenv(metricsAddrEnv)
	if metricsAddr == "" {
		metricsAddr = defaultMetricsAddr
	}
	metricsLn, err := net.Listen("tcp", metricsAddr)
	if err != nil {
		server.PrintAndDie(fmt.Sprintf("%s: broker metrics endpoint (%s=%q): %s", exe, metricsAddrEnv, metricsAddr, err))
	}

	// Create the server with appropriate options.
	s, err := server.NewServer(opts)
	if err != nil {
		server.PrintAndDie(fmt.Sprintf("%s: %s", exe, err))
	}

	// Configure the logger based on the flags.
	s.ConfigureLogger()

	// DeviceChain: the signal loop starts HERE, before Run, where upstream's starts: its handler
	// is installed at the end of NewServer. Run blocks through JetStream recovery, which on a
	// single server with a large store takes minutes, and a SIGTERM (or the preStop hook's
	// SIGUSR2) that waited for Run to return would be ignored for all of it, until Kubernetes
	// killed the process mid-recovery. A SIGHUP in that window reloads like any other; the
	// probe that follows it records an error until the listener is up.
	b := newBroker(s, opts, newBrokerMetrics())
	b.serveMetrics(metricsLn)
	b.handleSignals(sigs)

	// Start things up. Block here until done.
	if err := server.Run(s); err != nil {
		server.PrintAndDie(err.Error())
	}

	// DeviceChain: the watcher, which waits for the server to be ready before its first probe.
	go b.watch()

	s.WaitForShutdown()
}
