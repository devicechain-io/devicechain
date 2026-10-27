// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/config"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/devicechain-io/dc-user-management/schema"
)

// The `break-glass` subcommand: this binary, run by hand INSIDE a user-management pod
// (`dcctl instances break-glass mfa-reset` execs it), to take a second factor off an operator
// identity when nobody can sign in to do it through the admin API. The case the admin API cannot
// cover is an enrolled SOLE superuser, who has no one to reset them.
//
// 🔴 ITS AUTHORITY IS THE CLUSTER CREDENTIAL THAT RAN IT, and everything below is about not
// widening that. It runs only inside a pod of this service (the Kubernetes service environment and
// this area's own functional-area name, read from the container's environment), only with the
// mounted instance configuration and an instance root key that decodes (so it can open the sealed
// seed it is about to delete). The account's password is a new value it ALWAYS sets: generated here
// and printed once, or one line on stdin, never an argument, which a process list shows. It refuses
// an identity that holds no system role. It starts no server and opens no listener, and it runs the
// store code the service itself runs, so there is no second copy of the reset to drift.

const breakGlassUsage = `usage: user-management break-glass mfa-reset --email <address> [--password-stdin]

Removes the second factor from an identity that holds a system role, sets a NEW password and ends
every session the identity has. The new password is generated and printed once, or read as one line
from stdin with --password-stdin. Run it inside a user-management pod (dcctl instances break-glass
mfa-reset does).`

// breakGlassResetter is the one operation the subcommand needs from the booted service.
type breakGlassResetter interface {
	BreakGlassResetMfa(ctx context.Context, email, newPassword string) (*iam.Identity, error)
}

// breakGlassEnv is what the subcommand reads from its surroundings, so a test can run it without a
// pod: the environment, the streams, and how the service is booted.
type breakGlassEnv struct {
	getenv         func(string) string
	stdin          io.Reader
	stdout, stderr io.Writer
	// open boots the store code from the pod's mounted configuration and returns it with a close.
	open func(ctx context.Context) (breakGlassResetter, func(), error)
}

// productionBreakGlassEnv is the real surroundings.
func productionBreakGlassEnv() breakGlassEnv {
	return breakGlassEnv{
		getenv: os.Getenv, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		open: openBreakGlass,
	}
}

// runBreakGlass runs `break-glass <args>` and returns the process exit status.
func runBreakGlass(ctx context.Context, args []string, env breakGlassEnv) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(env.stderr, "break-glass: "+format+"\n", a...)
		return 1
	}
	if len(args) == 0 || args[0] != "mfa-reset" {
		fmt.Fprintln(env.stderr, breakGlassUsage)
		return 2
	}
	fs := flag.NewFlagSet("break-glass mfa-reset", flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	email := fs.String("email", "", "the identity's address")
	passwordStdin := fs.Bool("password-stdin", false, "read the new password as one line from stdin")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintln(env.stderr, breakGlassUsage)
		return 2
	}
	if strings.TrimSpace(*email) == "" || fs.NArg() != 0 {
		fmt.Fprintln(env.stderr, breakGlassUsage)
		return 2
	}

	// Only inside a pod of this service.
	if env.getenv("KUBERNETES_SERVICE_HOST") == "" {
		return fail("this runs only inside a user-management pod (dcctl instances break-glass mfa-reset runs it there); " +
			"no Kubernetes service environment was found")
	}
	if area := env.getenv(core.ENV_MS_FUNCTIONAL_AREA); area != "user-management" {
		return fail("this runs only inside a user-management pod; this container's functional area is %q", area)
	}

	password, generated, err := breakGlassPassword(env.stdin, *passwordStdin)
	if err != nil {
		return fail("%v", err)
	}

	r, closeFn, err := env.open(ctx)
	if err != nil {
		return fail("%v", err)
	}
	defer closeFn()

	id, err := r.BreakGlassResetMfa(ctx, *email, password)
	switch {
	case err == nil:
	case errors.Is(err, identity.ErrBreakGlassNotSystem), errors.Is(err, identity.ErrBreakGlassNeedsPassword),
		errors.Is(err, identity.ErrIdentityNotFound), errors.Is(err, identity.ErrMfaNotEnrolled):
		return fail("%v", err)
	default:
		return fail("the reset did not complete and nothing was changed: %v", err)
	}
	fmt.Fprintf(env.stdout, "Second factor removed for %s, password replaced, every session ended. Recorded as mfa_reset by %s.\n",
		id.Email, identity.BreakGlassActor)
	if generated {
		fmt.Fprintf(env.stdout, "New password (shown once, not stored anywhere else): %s\n", password)
	} else {
		fmt.Fprintln(env.stdout, "The password is the one read from stdin.")
	}
	return 0
}

// breakGlassPasswordLength is the generated password's length. The alphabet below has 56
// characters, so this is about 139 bits.
const breakGlassPasswordLength = 24

// breakGlassAlphabet leaves out the characters people misread when typing a password from a
// terminal (0/O, 1/l/I) and every character a shell treats specially.
const breakGlassAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// breakGlassPassword returns the new password: the first line of stdin when asked, else a
// generated one. An empty line is refused: the password is always rotated.
func breakGlassPassword(stdin io.Reader, fromStdin bool) (password string, generated bool, err error) {
	if fromStdin {
		line, rerr := bufio.NewReaderSize(stdin, 4096).ReadString('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return "", false, fmt.Errorf("reading the password from stdin: %w", rerr)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return "", false, errors.New("--password-stdin was given and stdin held no password; the password is always replaced")
		}
		return line, false, nil
	}
	out := make([]byte, breakGlassPasswordLength)
	max := big.NewInt(int64(len(breakGlassAlphabet)))
	for i := range out {
		n, rerr := rand.Int(rand.Reader, max)
		if rerr != nil {
			return "", false, fmt.Errorf("generating a password: %w", rerr)
		}
		out[i] = breakGlassAlphabet[n.Int64()]
	}
	return string(out), true, nil
}

// openBreakGlass boots the store code the way the service does, from the pod's mounted files, and
// stops short of everything else: no broker, no listener, no session store. It refuses unless the
// instance root key is present and decodes, because without it the sealed seed cannot be opened and
// a reset would be a lie about what it removed.
func openBreakGlass(ctx context.Context) (breakGlassResetter, func(), error) {
	ms := &core.Microservice{
		InstanceId:       os.Getenv(core.ENV_INSTANCE_ID),
		MicroserviceId:   os.Getenv(core.ENV_MICROSERVICE_ID),
		MicroserviceName: os.Getenv(core.ENV_MICROSERVICE_NAME),
		FunctionalArea:   os.Getenv(core.ENV_MS_FUNCTIONAL_AREA),
	}
	if err := ms.LoadInstanceConfiguration(); err != nil {
		return nil, nil, fmt.Errorf("reading the pod's instance configuration (%s): %w", core.InstanceConfigPath, err)
	}
	if err := ms.LoadMicroserviceConfiguration(); err != nil {
		return nil, nil, fmt.Errorf("reading the pod's service configuration: %w", err)
	}
	secretsCfg := ms.InstanceConfiguration.Infrastructure.Secrets
	if _, err := secretsCfg.DecodedRootKey(); err != nil {
		return nil, nil, fmt.Errorf("the instance root key is not available in this pod: %w", err)
	}
	cfg := &config.UserManagementConfiguration{}
	if err := core.LoadConfiguration(ms.MicroserviceConfigurationRaw, cfg); err != nil {
		return nil, nil, fmt.Errorf("the service configuration is invalid: %w", err)
	}
	db := rdb.NewRdbManager(ms, core.NewNoOpLifecycleCallbacks(), schema.Migrations,
		ms.InstanceConfiguration.Persistence.Rdb, cfg.RdbConfiguration)
	if err := db.Initialize(ctx); err != nil {
		return nil, nil, fmt.Errorf("opening the relational store: %w", err)
	}
	store, err := secrets.NewFromConfig(ctx, secretsCfg, db.Database)
	if err != nil {
		return nil, nil, fmt.Errorf("opening the secret store: %w", err)
	}
	m := identity.NewManager(ms, db, nil, store, 0, 0, "", identity.BootstrapConfig{}, nil)
	return m, func() {}, nil
}
