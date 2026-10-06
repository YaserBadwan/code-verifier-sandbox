package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// how long cleanup calls get, they run on a fresh context so a cancelled request still cleans up
const cleanupTimeout = 30 * time.Second

type Docker struct {
	cli     *client.Client
	host    string
	runtime string
	log     *slog.Logger
}

// NewDocker connects to the daemon and fails early with a readable error when it is not running.
func NewDocker(ctx context.Context, host, runtime string, log *slog.Logger) (*Docker, error) {
	opts := []client.Opt{client.FromEnv}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	d := &Docker{cli: cli, host: cli.DaemonHost(), runtime: runtime, log: log}

	if err := d.Ping(ctx); err != nil {
		cli.Close()
		return nil, err
	}
	if runtime != "" {
		info, err := cli.Info(ctx, client.InfoOptions{})
		if err != nil {
			cli.Close()
			return nil, fmt.Errorf("reading docker info: %w", err)
		}
		if _, ok := info.Info.Runtimes[runtime]; !ok {
			cli.Close()
			return nil, fmt.Errorf("docker runtime %q is not installed on this daemon", runtime)
		}
	}
	return d, nil
}

func (d *Docker) Host() string { return d.host }

func (d *Docker) Close() error { return d.cli.Close() }

func (d *Docker) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("cannot reach Docker at %s, make sure Docker Desktop (or the docker daemon) is running: %w", d.host, err)
	}
	return nil
}

func (d *Docker) CreateVolume(ctx context.Context, labels map[string]string) (string, error) {
	// a create cut off halfway can still finish on the daemon and we would never learn the
	// name, so let it complete and clean up afterwards if the caller is gone
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	res, err := d.cli.VolumeCreate(createCtx, client.VolumeCreateOptions{Labels: withManagedLabel(labels)})
	if err != nil {
		return "", fmt.Errorf("creating volume: %w", err)
	}
	if ctx.Err() != nil {
		d.RemoveVolume(ctx, res.Volume.Name)
		return "", ctx.Err()
	}
	return res.Volume.Name, nil
}

func (d *Docker) RemoveVolume(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	_, err := d.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("removing volume %s: %w", name, err)
	}
	return nil
}

func withManagedLabel(labels map[string]string) map[string]string {
	out := map[string]string{LabelManaged: "true"}
	for k, v := range labels {
		out[k] = v
	}
	return out
}

func (d *Docker) Run(ctx context.Context, spec Spec) (Result, error) {
	if len(spec.Files) > 0 && (spec.Volume == "" || !spec.WritableVolume) {
		return Result{}, errors.New("copying files needs a writable volume")
	}
	if spec.Timeout <= 0 || spec.OutputLimit <= 0 || spec.MaxFileBytes <= 0 {
		return Result{}, errors.New("timeout, output limit and max file size must be set")
	}

	if spec.Volume != "" && !spec.WritableVolume {
		// docker would quietly create a new empty volume with this name instead of failing
		if _, err := d.cli.VolumeInspect(ctx, spec.Volume, client.VolumeInspectOptions{}); err != nil {
			if cerrdefs.IsNotFound(err) {
				return Result{}, ErrVolumeMissing
			}
			return Result{}, fmt.Errorf("checking volume: %w", err)
		}
	}

	createCtx, cancelCreate := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	created, err := d.cli.ContainerCreate(createCtx, client.ContainerCreateOptions{
		Config:     d.containerConfig(spec),
		HostConfig: d.hostConfig(spec),
	})
	cancelCreate()
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return Result{}, fmt.Errorf("%w (%s)", ErrImageMissing, spec.Image)
		}
		return Result{}, fmt.Errorf("creating container: %w", err)
	}
	id := created.ID
	defer d.removeContainer(id)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if len(spec.Files) > 0 {
		archive, err := tarFiles(spec.Files)
		if err != nil {
			return Result{}, err
		}
		_, err = d.cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{
			DestinationPath: WorkspaceMount,
			Content:         archive,
			// keeps the uid from the tar headers so the sandbox user owns its files
			CopyUIDGID: true,
		})
		if err != nil {
			return Result{}, fmt.Errorf("copying files into container: %w", err)
		}
	}

	// attach before starting, otherwise fast programs can exit before we are listening
	attach, err := d.cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{
		Stream: true, Stdin: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		return Result{}, fmt.Errorf("attaching to container: %w", err)
	}
	defer attach.Close()

	stdout := &cappedBuffer{limit: spec.OutputLimit}
	stderr := &cappedBuffer{limit: spec.OutputLimit}
	readDone := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(stdout, stderr, attach.Reader)
		readDone <- err
	}()

	if _, err := d.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return Result{}, fmt.Errorf("starting container: %w", err)
	}
	started := time.Now()

	// a program that never reads stdin blocks this write, closing the attach unblocks it
	go func() {
		if len(spec.Stdin) > 0 {
			attach.Conn.Write(spec.Stdin)
		}
		attach.CloseWrite()
	}()

	waitCtx, cancelWait := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWait()
	wait := d.cli.ContainerWait(waitCtx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})

	timer := time.NewTimer(spec.Timeout)
	defer timer.Stop()

	var res Result
	// set once we kill the container, in case the daemon never reports it stopped
	var killDeadline <-chan time.Time
	killNow := func() {
		d.kill(id)
		if killDeadline == nil {
			killDeadline = time.After(cleanupTimeout)
		}
	}
	exited := false
	for !exited {
		select {
		case w := <-wait.Result:
			if w.Error != nil && w.Error.Message != "" {
				return Result{}, fmt.Errorf("waiting for container: %s", w.Error.Message)
			}
			res.ExitCode = int(w.StatusCode)
			res.Duration = time.Since(started)
			exited = true
		case err := <-wait.Error:
			return Result{}, fmt.Errorf("waiting for container: %w", err)
		case err := <-readDone:
			readDone = nil
			if errors.Is(err, errOutputLimit) {
				res.OutputLimitExceeded = true
				killNow()
			}
		case <-timer.C:
			res.TimedOut = true
			killNow()
		case <-killDeadline:
			return Result{}, errors.New("container did not stop after being killed")
		case <-ctx.Done():
			d.kill(id)
			return Result{}, ctx.Err()
		}
	}

	// give the output stream a moment to drain after exit, then force it closed
	if readDone != nil {
		select {
		case <-readDone:
		case <-time.After(2 * time.Second):
			attach.Close()
			<-readDone
		}
	}
	res.Stdout = stdout.Bytes()
	res.Stderr = stderr.Bytes()
	if stdout.exceeded || stderr.exceeded {
		res.OutputLimitExceeded = true
	}
	if res.TimedOut {
		res.Duration = spec.Timeout
	}

	inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if info, err := d.cli.ContainerInspect(inspectCtx, id, client.ContainerInspectOptions{}); err == nil && info.Container.State != nil {
		res.OOMKilled = info.Container.State.OOMKilled
	}
	return res, nil
}

func (d *Docker) containerConfig(spec Spec) *container.Config {
	env := append([]string{"HOME=/tmp", "TMPDIR=/tmp", "LANG=C.UTF-8"}, spec.Env...)
	return &container.Config{
		Image:        fullRef(spec.Image),
		Cmd:          spec.Cmd,
		Env:          env,
		User:         fmt.Sprintf("%d:%d", sandboxUID, sandboxUID),
		WorkingDir:   WorkDir,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		StdinOnce:    true,
		Labels:       withManagedLabel(spec.Labels),
	}
}

func (d *Docker) hostConfig(spec Spec) *container.HostConfig {
	initProcess := true
	pids := spec.PidsLimit
	tmpfs := "rw,nosuid,nodev,noexec,size=" + spec.TmpfsSize
	if spec.TmpfsExec {
		tmpfs = "rw,nosuid,nodev,exec,size=" + spec.TmpfsSize
	}
	hc := &container.HostConfig{
		// loopback only. The old NetworkDisabled flag is not used because it also skips
		// writing resolv.conf, which makes DNS lookups hang for 5 seconds
		NetworkMode:    "none",
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		// tini as pid 1 reaps zombies and takes every leftover process down with it
		Init:    &initProcess,
		Runtime: d.runtime,
		Tmpfs:   map[string]string{"/tmp": tmpfs},
		// there is no network, so make DNS lookups fail fast instead of hanging for 5 seconds
		DNSOptions: []string{"timeout:1", "attempts:1"},
		// output is read straight from the attach stream, so skip writing logs to disk
		LogConfig: container.LogConfig{Type: "none"},
		Resources: container.Resources{
			Memory: spec.MemoryBytes,
			// same as Memory means no swap at all
			MemorySwap: spec.MemoryBytes,
			NanoCPUs:   spec.NanoCPUs,
			PidsLimit:  &pids,
			Ulimits: []*container.Ulimit{
				{Name: "nofile", Soft: 1024, Hard: 1024},
				{Name: "core", Soft: 0, Hard: 0},
				{Name: "fsize", Soft: spec.MaxFileBytes, Hard: spec.MaxFileBytes},
			},
		},
	}
	if spec.Volume != "" {
		hc.Mounts = []mount.Mount{{
			Type:     mount.TypeVolume,
			Source:   spec.Volume,
			Target:   WorkspaceMount,
			ReadOnly: !spec.WritableVolume,
		}}
	}
	return hc
}

// builds box/ owned by the sandbox user with the files inside it
func tarFiles(files []File) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	now := time.Now()
	dir := path.Base(WorkDir)
	err := tw.WriteHeader(&tar.Header{
		Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755,
		Uid: sandboxUID, Gid: sandboxUID, ModTime: now,
	})
	for _, f := range files {
		if err != nil {
			break
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		err = tw.WriteHeader(&tar.Header{
			Name: dir + "/" + f.Name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(f.Data)),
			Uid: sandboxUID, Gid: sandboxUID, ModTime: now,
		})
		if err == nil {
			_, err = tw.Write(f.Data)
		}
	}
	if err == nil {
		err = tw.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("packing source files: %w", err)
	}
	return &buf, nil
}

func (d *Docker) kill(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := d.cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: "KILL"})
	if err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
		d.log.Warn("killing container failed", "container", shortID(id), "error", err)
	}
}

func (d *Docker) removeContainer(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		d.log.Warn("removing container failed, it will be cleaned up on next start", "container", shortID(id), "error", err)
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
