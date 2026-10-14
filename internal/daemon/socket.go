package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Akkanbe/gotwamp/internal/config"
)

// socketDirMode is the mode of a socket directory gotwampd creates itself;
// systemd's RuntimeDirectory= normally creates it (SPEC §9.10).
const socketDirMode = 0o750

// listenUnix listens on the Unix domain socket at path with permission mode
// and, unless empty, group (SPEC §9.10). A missing parent directory is
// created with mode 0750; an existing one is left as it is, with a warning
// when anyone may replace the socket in it. A stale socket left by a crashed
// daemon is removed; a socket another daemon still serves, or a file that is
// not a socket, is an error. The socket is removed when the listener closes.
//
//declscope:package // Start opens the API socket through it
func listenUnix(path string, mode fs.FileMode, group string, log *slog.Logger) (net.Listener, error) {
	dir := filepath.Dir(path)
	switch di, err := os.Stat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, socketDirMode); err != nil {
			return nil, fmt.Errorf("api socket directory: %w", err)
		}
		log.Info("created the api socket directory", "dir", dir, "mode", fs.FileMode(socketDirMode))
	case err != nil:
		return nil, fmt.Errorf("api socket directory: %w", err)
	case !di.IsDir():
		return nil, fmt.Errorf("api socket directory %s is not a directory", dir)
	case di.Mode().Perm()&0o002 != 0 && di.Mode()&fs.ModeSticky == 0:
		log.Warn("the api socket directory is writable by every user without the sticky bit; anyone can replace the socket",
			"dir", dir, "mode", di.Mode().Perm())
	}
	fi, err := os.Lstat(path)
	switch {
	case err == nil && fi.Mode().Type() != fs.ModeSocket:
		return nil, fmt.Errorf("api socket %s exists and is not a socket", path)
	case err == nil:
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("api socket %s is in use by another gotwampd", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale api socket: %w", err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("api socket: %w", err)
	}

	gid := -1
	if group != "" {
		g, err := user.LookupGroup(group)
		switch {
		case err != nil && group == config.DefaultSocketGroup:
			// The default group is there once the package is installed.
			log.Warn("api socket group missing; keeping the process's group", "group", group, "error", err)
		case err != nil:
			return nil, fmt.Errorf("api socket group: %w", err)
		default:
			if gid, err = strconv.Atoi(g.Gid); err != nil {
				return nil, fmt.Errorf("api socket group %s: gid %q: %w", group, g.Gid, err)
			}
		}
	}

	// The umask makes the socket appear with no more than mode, so that no
	// one outside mode can connect before the chmod below.
	old := syscall.Umask(int(0o777 &^ mode.Perm()))
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("api socket: %w", err)
	}
	if err := os.Chmod(path, mode.Perm()); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("api socket mode: %w", err)
	}
	if gid >= 0 {
		err := os.Chown(path, -1, gid)
		switch {
		case err != nil && group == config.DefaultSocketGroup:
			log.Warn("api socket group not applied; keeping the process's group", "group", group, "error", err)
		case err != nil:
			_ = ln.Close()
			return nil, fmt.Errorf("api socket group: %w", err)
		}
	}
	return ln, nil
}
