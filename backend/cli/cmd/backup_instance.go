// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/devicechain-io/dc-k8s/functionalarea"
	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dcctl/backup"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/nats-io/nats.go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type backupOptions struct {
	InstanceID, KubeContext, Destination, NatsURL, BlobDirectory string
	RestorePlan                                                  *backup.RestorePlan
	PlanOutput                                                   io.Writer
}

func backupInstance(ctx context.Context, opts backupOptions) error {
	if opts.InstanceID == "" || (opts.Destination == "" && opts.RestorePlan == nil) {
		return fmt.Errorf("instance and destination are required")
	}
	if opts.RestorePlan == nil {
		if _, err := os.Lstat(opts.Destination); err == nil {
			return fmt.Errorf("backup destination already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	// The public resolver overlays the installed per-login Secrets, including the
	// broker administrator, and preserves the existing namespace/credential rules.
	cfg, err := bootstrap.DeployedInstanceConfig(ctx, opts.KubeContext, opts.InstanceID)
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("instance %q is not installed", opts.InstanceID)
	}
	cfg.ApplyDefaults()
	decl, err := bootstrap.ReadInstanceCR(ctx, opts.KubeContext, opts.InstanceID)
	if err != nil {
		return err
	}
	if decl == nil {
		return fmt.Errorf("instance declaration required to resolve enabled functional areas")
	}
	areas, err := bootstrap.ResolveEnabledAreas(decl.Spec.Profile, decl.Spec.ExtraFunctionalAreas)
	if err != nil {
		return err
	}
	if len(areas) == 0 {
		base, err := functionalarea.ResolveAndValidate(decl.Spec.Profile, nil)
		if err != nil {
			return err
		}
		for _, a := range base {
			areas = append(areas, string(a))
		}
	}
	bc, err := instanceBlobConfig(cfg.Infrastructure.Blob, opts.BlobDirectory, "filesystem blob backup requires --blob-directory pointing to a read-only mounted/exported instance PVC root")
	if err != nil {
		return err
	}
	url := opts.NatsURL
	if url == "" {
		restCfg, err := bootstrap.RestConfig(opts.KubeContext)
		if err != nil {
			return err
		}
		typed, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return err
		}
		ns := bootstrap.InstanceNamespace(opts.InstanceID)
		pods, err := typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=nats,app.kubernetes.io/instance=dc-nats,app.kubernetes.io/component=nats"})
		if err != nil {
			return err
		}
		if len(pods.Items) == 0 {
			return fmt.Errorf("no broker pod in %s", ns)
		}
		roundTripper, upgrader, err := spdy.RoundTripperFor(restCfg)
		if err != nil {
			return err
		}
		target := typed.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pods.Items[0].Name).SubResource("portforward").URL()
		dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, target)
		stop, ready := make(chan struct{}), make(chan struct{})
		defer close(stop)
		fw, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", cfg.Infrastructure.Nats.Port)}, stop, ready, io.Discard, io.Discard)
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- fw.ForwardPorts() }()
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case <-ready:
		case err := <-done:
			return fmt.Errorf("broker port-forward stopped: %v", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("broker port-forward did not become ready")
		}
		ports, err := fw.GetPorts()
		if err != nil {
			return err
		}
		if len(ports) != 1 {
			return fmt.Errorf("broker port-forward reported no unique port")
		}
		url = fmt.Sprintf("nats://127.0.0.1:%d", ports[0].Local)
	}
	nc, err := backupBroker(ctx, url, cfg.Infrastructure.Nats)
	if err != nil {
		return err
	}
	defer nc.Close()
	export := func(ctx context.Context, w io.Writer) error {
		return backup.ExportBlobs(ctx, bc, opts.InstanceID, w)
	}
	if opts.RestorePlan != nil {
		actions, err := opts.RestorePlan.Plan(ctx, nc, export, cfg.Infrastructure.Nats.ShardGroups, areas)
		if err != nil {
			return err
		}
		return backup.WritePlan(opts.PlanOutput, actions)
	}
	return backup.Create(ctx, nc, opts.InstanceID, opts.Destination, export, cfg.Infrastructure.Nats.ShardGroups, areas)
}

// instanceBlobConfig is the deployed blob store, with a filesystem backend read
// through the operator's local mount of the volume rather than the pods' path.
func instanceBlobConfig(bc config.BlobConfiguration, directory, missing string) (blob.Config, error) {
	if bc.Backend == "" || bc.Backend == blob.BackendFilesystem {
		if directory == "" {
			return blob.Config{}, fmt.Errorf("%s", missing)
		}
		bc.Directory = directory
	}
	return blob.Config{Backend: bc.Backend, Directory: bc.Directory, Bucket: bc.Bucket, Region: bc.Region, Endpoint: bc.Endpoint, UsePathStyle: bc.UsePathStyle}, nil
}

// restoreObjects applies a verified plan's object store to the installed
// instance. It needs no broker: objects live in the blob store alone.
func restoreObjects(ctx context.Context, opts backupOptions, overwrite bool) error {
	cfg, err := bootstrap.DeployedInstanceConfig(ctx, opts.KubeContext, opts.InstanceID)
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("instance %q is not installed", opts.InstanceID)
	}
	cfg.ApplyDefaults()
	bc, err := instanceBlobConfig(cfg.Infrastructure.Blob, opts.BlobDirectory, "filesystem blob restore requires --blob-directory pointing to a writable local mount of the instance's blob PVC root")
	if err != nil {
		return err
	}
	target, err := backup.NewObjectTarget(ctx, bc)
	if err != nil {
		return err
	}
	export := func(ctx context.Context, w io.Writer) error {
		return backup.ExportBlobs(ctx, bc, opts.InstanceID, w)
	}
	_, err = opts.RestorePlan.ApplyObjects(ctx, export, target, overwrite, opts.PlanOutput)
	// A missing instance directory is refused, never read as an empty store: a
	// mistyped mount path looks exactly like it. A freshly provisioned volume has
	// none until its first upload, so say what to do when that is the case.
	if errors.Is(err, fs.ErrNotExist) && bc.Directory != "" {
		return fmt.Errorf("%w\nIf %s is the instance's freshly provisioned blob volume, create the directory %q in it and run the restore again", err, bc.Directory, opts.InstanceID)
	}
	return err
}

// Match the diagnostic commands' transport policy: resolve TLS through the
// instance config, pin its server name even over loopback, and use the admin
// principal's own inbox and login. Never fall back from a missing area login.
func backupBroker(ctx context.Context, url string, cfg config.NatsConfiguration) (*nats.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := 10 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, max(time.Until(deadline), time.Millisecond))
	}
	opts := []nats.Option{nats.Name("dcctl-backup"), nats.NoReconnect(), nats.Timeout(timeout)}
	tlsConfig, err := cfg.TLSConfig(cfg.Hostname)
	if err != nil {
		return nil, err
	}
	if tlsConfig != nil {
		tlsConfig.ServerName = cfg.Hostname
		opts = append(opts, nats.Secure(tlsConfig))
	}
	user, password := cfg.Auth.User, cfg.Auth.Password
	if cfg.Auth.AreaLogins {
		user = natsauth.LoginUser(config.NatsAdminPrincipal)
		password = cfg.Auth.AreaPasswords[config.NatsAdminPrincipal]
		if password == "" {
			return nil, fmt.Errorf("instance broker administrator login is missing from dci-<instance>-nats-broker-admin")
		}
		opts = append(opts, nats.CustomInboxPrefix(messaging.InboxPrefix(config.NatsAdminPrincipal)))
	}
	if user != "" {
		opts = append(opts, nats.UserInfo(user, password))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		// The client quotes a URL it cannot parse whole, userinfo and all.
		return nil, installflow.RedactURLError(err)
	}
	return nc, nil
}
