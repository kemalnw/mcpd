//go:build linux

package filesystem

import (
	"bufio"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func (m *Manager) Info(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, fmt.Errorf("stat %q: %w", path, err)
	}
	fileType := FileTypeDirectory
	if !info.IsDir() {
		fileType, _, err = detectFileType(path)
		if err != nil {
			return FileInfo{}, err
		}
	}
	out := FileInfo{
		Path: path, Name: info.Name(), Size: info.Size(), Modified: info.ModTime(),
		IsDirectory: info.IsDir(), IsFile: info.Mode().IsRegular(), Permissions: fmt.Sprintf("%03o", info.Mode().Perm()), FileType: fileType,
	}
	var statx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME|unix.STATX_ATIME, &statx); err == nil {
		if statx.Mask&unix.STATX_BTIME != 0 {
			t := time.Unix(statx.Btime.Sec, int64(statx.Btime.Nsec)).UTC()
			out.Created = &t
		}
		if statx.Mask&unix.STATX_ATIME != 0 {
			t := time.Unix(statx.Atime.Sec, int64(statx.Atime.Nsec)).UTC()
			out.Accessed = &t
		}
	}
	if out.IsFile && fileType == FileTypeText {
		count, err := m.countFileLines(path)
		if err != nil {
			return FileInfo{}, err
		}
		last := count - 1
		appendPos := count
		out.LineCount = &count
		out.LastLine = &last
		out.AppendPosition = &appendPos
	}
	return out, nil
}

func (m *Manager) countFileLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), m.opts.MaxLineBytes)
	count := 0
	for scanner.Scan() {
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return count, nil
}
