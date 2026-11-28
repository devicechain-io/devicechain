// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/devicechain-io/dc-microservice/blob"
)

// ObjectTarget writes verified objects into an instance's blob store. Build one
// with NewObjectTarget; its write never makes a byte visible at a key before
// every byte has been checked against the verified backup index.
type ObjectTarget struct {
	backend string
	// write stores exactly src at key. Unless replace is set it must refuse a key
	// that exists by the time it commits: a restore never clobbers an object it
	// did not see and judge.
	write func(ctx context.Context, key string, want objectRecord, src io.Reader, replace bool) error
}

// NewObjectTarget opens the target store described by cfg. A filesystem
// Directory is the root of a WRITABLE local mount of the instance's blob volume
// (the directory that contains the instance directory); S3 uses the operator's
// standard AWS credential chain, as the export does.
func NewObjectTarget(ctx context.Context, cfg blob.Config) (*ObjectTarget, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Backend {
	case "", blob.BackendFilesystem:
		root, err := os.OpenRoot(cfg.Directory)
		if err != nil {
			return nil, err
		}
		return &ObjectTarget{backend: blob.BackendFilesystem, write: filesystemWriter(root)}, nil
	case blob.BackendS3:
		client, err := s3Client(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return &ObjectTarget{backend: blob.BackendS3, write: s3Writer(client, cfg.Bucket)}, nil
	default:
		return nil, fmt.Errorf("blob restore backend %q is unsupported", cfg.Backend)
	}
}

// ObjectRestore counts what ApplyObjects did.
type ObjectRestore struct {
	// Written objects were absent from, or (with overwrite) different on, the target.
	Written int
	// Matched objects were already byte-identical on the target and were left alone.
	Matched int
	// Kept objects are on the target but not in the backup; a restore never deletes.
	Kept int
}

// ApplyObjects restores the backup's object store into the target instance.
//
// Everything that can refuse is decided before the first write: the plan must
// be for the instance the backup was taken from, every saved object must be for
// the target's backend, and, unless overwrite is set, every object already on
// the target must be byte-identical to the backup's. Only missing objects (and,
// with overwrite, differing ones) are written, so a run that stopped part way is
// finished by running it again. Each object is staged and checked against the
// verified index before it is committed, and the target is exported again at
// the end and must hold every backup object.
//
// current exports the target store (as ExportBlobs does); progress receives one
// line per step and may be nil.
func (p *RestorePlan) ApplyObjects(ctx context.Context, current func(context.Context, io.Writer) error, target *ObjectTarget, overwrite bool, progress io.Writer) (ObjectRestore, error) {
	var res ObjectRestore
	if p == nil || p.manifest == nil || p.objects == nil || current == nil || target == nil {
		return res, fmt.Errorf("verified plan, target store and target reader required")
	}
	if progress == nil {
		progress = io.Discard
	}
	entry, ok := p.objectEntry()
	if !ok {
		return res, fmt.Errorf("backup has no object-store entry")
	}
	// The restored database names objects by reference, and a reference carries
	// the source instance's key prefix and backend. Writing the objects anywhere
	// else would leave every such reference dangling, so this is refused rather
	// than migrated.
	if p.manifest.Instance != p.instance {
		return res, fmt.Errorf("restoring objects into another instance is not implemented: object references in the restored database name instance %q, not %q", p.manifest.Instance, p.instance)
	}
	for _, key := range sortedKeys(p.objects) {
		ref, err := blob.ParseRef(p.objects[key].Ref)
		if err != nil {
			return res, err
		}
		if ref.Backend != target.backend {
			return res, fmt.Errorf("backup object %q was saved from the %s backend; the target stores objects on %s, and the restored database's references would not resolve", key, ref.Backend, target.backend)
		}
	}

	live, err := targetObjects(ctx, current, p.instance)
	if err != nil {
		return res, fmt.Errorf("target object store: %w", err)
	}
	var divergent, extra []string
	for _, key := range sortedKeys(live) {
		saved, ok := p.objects[key]
		switch {
		case !ok:
			extra = append(extra, key)
		case !sameObject(live[key], saved):
			divergent = append(divergent, key)
		}
	}
	if !overwrite && len(divergent)+len(extra) > 0 {
		first := append(slices.Clone(divergent), extra...)[0]
		return res, fmt.Errorf("target object store is not empty: %d object(s) differ from the backup and %d are not in it (first: %q); they may be newer than the backup. Pass --overwrite-target to replace the differing objects with the backup's bytes (objects not in the backup are kept)", len(divergent), len(extra), first)
	}
	todo := map[string]bool{}
	for key, saved := range p.objects {
		if obj, ok := live[key]; !ok || !sameObject(obj, saved) {
			todo[key] = true
		}
	}
	res.Matched, res.Kept = len(p.objects)-len(todo), len(extra)
	fmt.Fprintf(progress, "%s %q: %d to write, %d already match, %d kept (not in backup)\n", entry.Kind, entry.Name, len(todo), res.Matched, res.Kept)

	if len(todo) > 0 {
		if err := p.writeObjects(ctx, entry, todo, live, target, progress, &res); err != nil {
			return res, err
		}
	}

	after, err := targetObjects(ctx, current, p.instance)
	if err != nil {
		return res, fmt.Errorf("re-reading target object store: %w", err)
	}
	for _, key := range sortedKeys(p.objects) {
		if obj, ok := after[key]; !ok || !sameObject(obj, p.objects[key]) {
			return res, fmt.Errorf("after restore, target object %q does not match the backup", key)
		}
	}
	fmt.Fprintf(progress, "%s %q: restored, all %d backup objects match on the target\n", entry.Kind, entry.Name, len(p.objects))
	return res, nil
}

// writeObjects streams the object snapshot a second time and writes each object
// in todo. The bytes are checked again here, not only at PrepareRestore: the
// set on disk may have changed since it was verified.
func (p *RestorePlan) writeObjects(ctx context.Context, entry Entry, todo map[string]bool, live map[string]objectRecord, target *ObjectTarget, progress io.Writer, res *ObjectRestore) error {
	root, err := os.OpenRoot(p.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.Open(entry.Snapshot)
	if err != nil {
		return err
	}
	defer f.Close()
	total := len(todo)
	tr := tar.NewReader(f)
	for len(todo) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Name == "index.json" {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("non-regular object archive entry %q", h.Name)
		}
		key, found := strings.CutPrefix(h.Name, "objects/")
		want, indexed := p.objects[key]
		if !found || !indexed {
			return fmt.Errorf("object archive entry %q is not in the verified index", h.Name)
		}
		if !todo[key] {
			continue
		}
		_, exists := live[key]
		if err := target.write(ctx, key, want, tr, exists); err != nil {
			return fmt.Errorf("object %q: %w", key, err)
		}
		delete(todo, key)
		res.Written++
		fmt.Fprintf(progress, "%s %q: wrote %s (%d of %d)\n", entry.Kind, entry.Name, key, res.Written, total)
	}
	if len(todo) > 0 {
		return fmt.Errorf("backup object snapshot no longer holds %d verified object(s)", len(todo))
	}
	return nil
}

func (p *RestorePlan) objectEntry() (Entry, bool) {
	for _, e := range p.manifest.Entries {
		if e.Kind == "object-store" {
			return e, true
		}
	}
	return Entry{}, false
}

// sameObject compares content and backend; object identity is the map key.
func sameObject(a, b objectRecord) bool {
	ra, errA := blob.ParseRef(a.Ref)
	rb, errB := blob.ParseRef(b.Ref)
	return errA == nil && errB == nil && ra.Backend == rb.Backend && a.Size == b.Size && a.SHA256 == b.SHA256
}

func sortedKeys(m map[string]objectRecord) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// stage copies src to dst and refuses anything but exactly want's size and
// SHA-256. It reads at most one byte past the size, so a longer source is
// refused without being consumed whole.
func stage(dst io.Writer, src io.Reader, want objectRecord) error {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, want.Size+1))
	if err != nil {
		return err
	}
	if n != want.Size {
		return fmt.Errorf("size %d does not match the verified %d bytes", n, want.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want.SHA256 {
		return fmt.Errorf("SHA-256 %s does not match the verified %s", got, want.SHA256)
	}
	return nil
}

// filesystemWriter stages into a ".put-" temp file beside the object, the
// prefix the blob store itself uses for an in-flight write (and the one backup
// refuses as an incomplete upload, so a crashed restore cannot be exported as
// if it were whole). A new object is committed with a hard link, which fails if
// the key appeared in the meantime; a replacement is committed with a rename.
// All paths go through os.Root, so nothing resolves outside the store root.
func filesystemWriter(root *os.Root) func(context.Context, string, objectRecord, io.Reader, bool) error {
	return func(_ context.Context, key string, want objectRecord, src io.Reader, replace bool) error {
		dir := path.Dir(key)
		if err := root.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return err
		}
		tmp := path.Join(dir, ".put-restore-"+hex.EncodeToString(suffix[:]))
		f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		renamed := false
		defer func() {
			f.Close()
			if !renamed {
				_ = root.Remove(tmp)
			}
		}()
		if err := stage(f, src, want); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if replace {
			if err := root.Rename(tmp, key); err != nil {
				return err
			}
			renamed = true
			return nil
		}
		if err := root.Link(tmp, key); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("appeared on the target during the restore; refusing to overwrite it")
			}
			return err
		}
		return nil
	}
}

// s3Writer spools each object to a local temp file while checking it, so the
// upload has a known length and a seekable body, then puts it with the verified
// SHA-256 as the object checksum: the server refuses bytes that do not match.
// A new object is put with If-None-Match: *, so a key written since the target
// was read is not clobbered.
func s3Writer(client *s3.Client, bucket string) func(context.Context, string, objectRecord, io.Reader, bool) error {
	return func(ctx context.Context, key string, want objectRecord, src io.Reader, replace bool) error {
		sum, err := hex.DecodeString(want.SHA256)
		if err != nil {
			return err
		}
		spool, err := os.CreateTemp("", "dcctl-restore-*")
		if err != nil {
			return err
		}
		defer func() { spool.Close(); os.Remove(spool.Name()) }()
		if err := stage(spool, src, want); err != nil {
			return err
		}
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return err
		}
		in := &s3.PutObjectInput{
			Bucket:         aws.String(bucket),
			Key:            aws.String(key),
			Body:           spool,
			ContentLength:  aws.Int64(want.Size),
			ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum)),
		}
		if want.ContentType != "" {
			in.ContentType = aws.String(want.ContentType)
		}
		if !replace {
			in.IfNoneMatch = aws.String("*")
		}
		_, err = client.PutObject(ctx, in)
		return err
	}
}

// s3Client builds the client both the export and the restore use: the deployed
// bucket's region and endpoint, and the operator's standard credential chain.
func s3Client(ctx context.Context, cfg blob.Config) (*s3.Client, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	ac, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(ac, func(o *s3.Options) {
		o.UsePathStyle = cfg.UsePathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	}), nil
}
