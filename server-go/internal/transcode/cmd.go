package transcode

import (
	"io"
	"os"
	"os/exec"
)

// Cmd is the child-process contract the pipeline needs (os/exec-backed
// in production, faked in tests). Wait resolves once the process has
// exited, is safe from any goroutine and repeatable, mirroring the
// Deno `child.status` promise; every caller ignores Signal/Wait errors
// like the Deno try/catch around kill().
type Cmd interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	Signal(sig os.Signal) error
	Wait() (exitCode int, err error)
}

// SpawnFunc spawns a child process. The production implementation is
// deliberately NOT context-bound: children are stopped explicitly by
// attempt.stop (close stdout → SIGTERM → SIGKILL), and a context kill on
// client disconnect would skip the EPIPE-first ordering that unblocks a
// pipe-stalled ffmpeg.
type SpawnFunc func(name string, args []string) (Cmd, error)

// realSpawn runs name with explicit os.Pipes instead of exec's
// StdoutPipe: the parent fully owns its pipe ends, so cmd.Wait never
// races in-flight reads and stop() can close the stdout reader directly
// (the EPIPE half of the stop ordering).
func realSpawn(name string, args []string) (Cmd, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		return nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		stdinR.Close()
		stdinW.Close()
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return nil, err
	}
	// The child holds dup'ed copies now; dropping the parent's keeps EOF
	// (child exit) and EPIPE (reader closed) semantics exact.
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()
	c := &execCmd{
		cmd:    cmd,
		stdin:  stdinW,
		stdout: stdoutR,
		stderr: stderrR,
		done:   make(chan struct{}),
	}
	// Reap eagerly so a killed child never lingers as a zombie even if
	// no one calls Wait.
	go func() {
		err := cmd.Wait()
		c.waitErr = err
		if cmd.ProcessState != nil {
			c.code = cmd.ProcessState.ExitCode()
		}
		close(c.done)
	}()
	return c, nil
}

type execCmd struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	done    chan struct{}
	code    int
	waitErr error
}

func (c *execCmd) Stdin() io.WriteCloser      { return c.stdin }
func (c *execCmd) Stdout() io.ReadCloser      { return c.stdout }
func (c *execCmd) Stderr() io.ReadCloser      { return c.stderr }
func (c *execCmd) Signal(sig os.Signal) error { return c.cmd.Process.Signal(sig) }

func (c *execCmd) Wait() (int, error) {
	<-c.done
	return c.code, c.waitErr
}
