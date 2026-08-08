package hashline

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type opsNode struct {
	data []byte
	mode os.FileMode
}
type faultOps struct {
	mu    sync.Mutex
	files map[string]opsNode
	fault string
	calls []string
	temp  string
	exdev bool
}

func newFaultOps(files map[string]string) *faultOps {
	f := &faultOps{files: map[string]opsNode{}, temp: "/d/.tmp"}
	for p, s := range files {
		f.files[p] = opsNode{[]byte(s), 0640}
	}
	return f
}
func (f *faultOps) hit(s string) error {
	f.calls = append(f.calls, s)
	if f.fault == s {
		return errors.New("injected " + s)
	}
	return nil
}
func (f *faultOps) CreateTemp(d, p string) (filesystemFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.hit("createTemp"); e != nil {
		return nil, e
	}
	f.temp = filepath.Join(d, ".tmp")
	f.files[f.temp] = opsNode{mode: 0600}
	return &faultFile{ops: f, path: f.temp, kind: "file"}, nil
}
func (f *faultOps) Open(p string) (filesystemFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kind := "source"
	if p == "/d" {
		kind = "dir"
	} else if p == "/s" {
		kind = "sourceDir"
	}
	if e := f.hit(kind + "Open"); e != nil {
		return nil, e
	}
	if kind == "source" {
		if _, ok := f.files[p]; !ok {
			return nil, os.ErrNotExist
		}
	}
	return &faultFile{ops: f, path: p, kind: kind}, nil
}
func (f *faultOps) Lstat(p string) (os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeInfo{filepath.Base(p), int64(len(n.data)), n.mode}, nil
}
func (f *faultOps) Remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := "cleanupRemove"
	if p == "/d/a" || p == "/s/a" {
		key = "sourceRemove"
	}
	if e := f.hit(key); e != nil {
		return e
	}
	delete(f.files, p)
	return nil
}
func (f *faultOps) Rename(a, b string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.hit("publish"); e != nil {
		return e
	}
	f.files[b] = f.files[a]
	delete(f.files, a)
	return nil
}
func (f *faultOps) Link(a, b string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exdev && a != "/d/.tmp" {
		f.calls = append(f.calls, "linkEXDEV")
		return syscall.EXDEV
	}
	if e := f.hit("publish"); e != nil {
		return e
	}
	if _, ok := f.files[b]; ok {
		return os.ErrExist
	}
	f.files[b] = f.files[a]
	return nil
}

type faultFile struct {
	ops        *faultOps
	path, kind string
	off        int
}

func (x *faultFile) Read(p []byte) (int, error) {
	x.ops.mu.Lock()
	defer x.ops.mu.Unlock()
	b := x.ops.files[x.path].data
	if x.off >= len(b) {
		return 0, io.EOF
	}
	n := copy(p, b[x.off:])
	x.off += n
	return n, nil
}
func (x *faultFile) Write(p []byte) (int, error) {
	x.ops.mu.Lock()
	defer x.ops.mu.Unlock()
	if e := x.ops.hit("write"); e != nil {
		return 0, e
	}
	n := len(p)
	if x.ops.fault == "shortWrite" {
		n--
	}
	v := x.ops.files[x.path]
	v.data = append([]byte(nil), p[:n]...)
	x.ops.files[x.path] = v
	return n, nil
}
func (x *faultFile) Stat() (os.FileInfo, error) {
	x.ops.mu.Lock()
	defer x.ops.mu.Unlock()
	if e := x.ops.hit("stat"); e != nil {
		return nil, e
	}
	n := x.ops.files[x.path]
	return fakeInfo{filepath.Base(x.path), int64(len(n.data)), n.mode}, nil
}
func (x *faultFile) Chmod(m os.FileMode) error {
	x.ops.mu.Lock()
	defer x.ops.mu.Unlock()
	if e := x.ops.hit("chmod"); e != nil {
		return e
	}
	n := x.ops.files[x.path]
	n.mode = m
	x.ops.files[x.path] = n
	return nil
}
func (x *faultFile) Sync() error {
	x.ops.mu.Lock()
	defer x.ops.mu.Unlock()
	return x.ops.hit(x.kind + "Sync")
}
func (x *faultFile) Close() error {
	x.ops.mu.Lock()
	defer x.ops.mu.Unlock()
	return x.ops.hit(x.kind + "Close")
}
func (x *faultFile) Name() string { return x.path }

type fakeInfo struct {
	name string
	size int64
	mode os.FileMode
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return i.size }
func (i fakeInfo) Mode() os.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeInfo) Sys() any           { return nil }

func TestAtomicReplaceEveryFilesystemStagePreservesOrReportsCommittedState(t *testing.T) {
	for _, stage := range []string{"createTemp", "write", "shortWrite", "chmod", "fileSync", "fileClose", "publish", "dirOpen", "dirSync", "dirClose"} {
		t.Run(stage, func(t *testing.T) {
			f := newFaultOps(map[string]string{"/d/a": "old"})
			f.fault = stage
			err := atomicReplaceWithOps(f, "/d/a", []byte("new"), 0751)
			n := f.files["/d/a"]
			committed := stage == "dirOpen" || stage == "dirSync" || stage == "dirClose"
			if committed {
				var u *CommitUncertainError
				if !errors.As(err, &u) || string(n.data) != "new" || n.mode != 0751 {
					t.Fatalf("err=%T %v node=%+v", err, err, n)
				}
			} else if err == nil || string(n.data) != "old" {
				t.Fatalf("err=%v original=%q", err, n.data)
			}
			if _, ok := f.files[f.temp]; ok && !committed {
				t.Fatalf("temporary file leaked: files=%v", f.files)
			}
		})
	}
}

func TestExclusiveCreateFaultsLeaveNoTemporaryFiles(t *testing.T) {
	for _, stage := range []string{"createTemp", "write", "shortWrite", "chmod", "fileSync", "fileClose", "publish", "dirOpen", "dirSync", "dirClose"} {
		t.Run(stage, func(t *testing.T) {
			f := newFaultOps(nil)
			f.fault = stage
			err := atomicCreateWithOps(f, "/d/a", []byte("new"), 0751)
			n, exists := f.files["/d/a"]
			committed := stage == "dirOpen" || stage == "dirSync" || stage == "dirClose"
			if committed {
				var uncertain *CommitUncertainError
				if !errors.As(err, &uncertain) || !exists || string(n.data) != "new" || n.mode != 0751 || !strings.Contains(err.Error(), "uncertain") {
					t.Fatalf("publication uncertainty must be actual and no-retry-classified: err=%T %v exists=%v node=%+v", err, err, exists, n)
				}
			} else if err == nil || exists {
				t.Fatalf("pre-publication failure created destination: err=%v exists=%v node=%+v", err, exists, n)
			}
			for path := range f.files {
				if strings.Contains(filepath.Base(path), ".tmp") || strings.HasPrefix(filepath.Base(path), ".atenea-") {
					t.Fatalf("temporary file leaked after %s: files=%v", stage, f.files)
				}
			}
		})
	}
}

func TestDurableRemoveEveryFilesystemStageHasExactVisibility(t *testing.T) {
	for _, stage := range []string{"sourceRemove", "dirOpen", "dirSync", "dirClose"} {
		t.Run(stage, func(t *testing.T) {
			f := newFaultOps(map[string]string{"/d/a": "old"})
			f.fault = stage
			err := durableRemoveWithOps(f, "/d/a")
			_, exists := f.files["/d/a"]
			if stage == "sourceRemove" {
				if err == nil || !exists {
					t.Fatalf("err=%v exists=%v", err, exists)
				}
			} else {
				var u *CommitUncertainError
				if !errors.As(err, &u) || exists {
					t.Fatalf("err=%T %v exists=%v", err, err, exists)
				}
			}
		})
	}
}

func TestSameFilesystemMoveEveryStageNoOverwriteAndDirectoryDurability(t *testing.T) {
	stages := []string{"publish", "dirOpen", "dirSync", "dirClose", "sourceRemove"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			f := newFaultOps(map[string]string{"/s/a": "A"})
			f.fault = stage
			err := durableMoveWithOps(f, "/s/a", "/d/b")
			_, src := f.files["/s/a"]
			dst, landed := f.files["/d/b"]
			if stage == "publish" {
				if err == nil || !src || landed {
					t.Fatalf("err=%v src=%v dst=%v", err, src, landed)
				}
			} else if !landed || string(dst.data) != "A" || !src {
				t.Fatalf("err=%v files=%v", err, f.files)
			}
			if stage != "publish" {
				var dc *DestinationCommittedError
				if !errors.As(err, &dc) {
					t.Fatalf("error %T, want DestinationCommittedError", err)
				}
			}
		})
	}
	f := newFaultOps(map[string]string{"/d/a": "A", "/d/b": "B"})
	if err := durableMoveWithOps(f, "/d/a", "/d/b"); err == nil || string(f.files["/d/a"].data) != "A" || string(f.files["/d/b"].data) != "B" {
		t.Fatalf("overwrite guard: err=%v files=%v", err, f.files)
	}
	f = newFaultOps(map[string]string{"/d/a": "A"})
	if err := durableMoveWithOps(f, "/d/a", "/d/b"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.Join(f.calls, ","), "dirSync") != 1 {
		t.Fatalf("calls=%v", f.calls)
	}
}

func TestCrossFilesystemMoveEveryStageHasNoLossAndExactDuplicateState(t *testing.T) {
	stages := []string{"sourceOpen", "stat", "createTemp", "write", "shortWrite", "chmod", "fileSync", "fileClose", "publish", "dirOpen", "dirSync", "dirClose", "sourceRemove", "sourceDirOpen", "sourceDirSync", "sourceDirClose"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			f := newFaultOps(map[string]string{"/s/a": "A"})
			f.exdev = true
			f.fault = stage
			err := durableMoveWithOps(f, "/s/a", "/d/b")
			_, src := f.files["/s/a"]
			dst, landed := f.files["/d/b"]
			published := stage == "dirOpen" || stage == "dirSync" || stage == "dirClose" || stage == "sourceRemove" || stage == "sourceDirOpen" || stage == "sourceDirSync" || stage == "sourceDirClose"
			expectSource := stage != "sourceDirOpen" && stage != "sourceDirSync" && stage != "sourceDirClose"
			if err == nil || src != expectSource || landed != published || (landed && string(dst.data) != "A") {
				t.Fatalf("stage=%s err=%v src=%v dst=%q files=%v", stage, err, src, dst.data, f.files)
			}
			if _, tmp := f.files[f.temp]; tmp {
				t.Fatalf("temp leaked: %v", f.files)
			}
			if stage == "sourceDirOpen" || stage == "sourceDirSync" || stage == "sourceDirClose" {
				var uncertain *CommitUncertainError
				if !errors.As(err, &uncertain) {
					t.Fatalf("error=%T %v", err, err)
				}
			} else if published {
				var dc *DestinationCommittedError
				if !errors.As(err, &dc) {
					t.Fatalf("error=%T %v", err, err)
				}
			}
		})
	}
	f := newFaultOps(map[string]string{"/s/a": "A"})
	f.exdev = true
	if err := durableMoveWithOps(f, "/s/a", "/d/b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.files["/s/a"]; ok || string(f.files["/d/b"].data) != "A" {
		t.Fatalf("files=%v", f.files)
	}
}

func TestFilesystemPathLocksInverseOverlapAndSamePathDoNotDeadlock(t *testing.T) {
	sets := [][]string{{"a", "b"}, {"b", "a"}, {"a", "b", "c"}, {"b"}, {"a", "a"}}
	done := make(chan struct{})
	for n := 0; n < 20; n++ {
		for _, s := range sets {
			go func(p []string) { u := lockPatchPaths(p...); time.Sleep(time.Microsecond); u(); done <- struct{}{} }(s)
		}
	}
	for range 20 * len(sets) {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("path lock deadlock")
		}
	}
	patchPathLocks.Lock()
	defer patchPathLocks.Unlock()
	if len(patchPathLocks.m) != 0 {
		t.Fatalf("lock entries leaked: %v", patchPathLocks.m)
	}
}
