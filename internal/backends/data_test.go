package backends_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/privacyfence/peek/internal/backends"
	"github.com/privacyfence/peek/internal/backends/backendstest"
)

type tarEntry struct {
	Name     string
	Type     byte
	Body     string
	Linkname string
}

func makeTar(t *testing.T, uid, gid int, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.Type
		if typ == 0 {
			typ = tar.TypeReg
			if strings.HasSuffix(e.Name, "/") {
				typ = tar.TypeDir
			}
		}
		h := &tar.Header{Name: e.Name, Typeflag: typ, Mode: 0o644, Uid: uid, Gid: gid, Uname: "u", Gname: "g", Linkname: e.Linkname}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.Body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readTar(t *testing.T, data []byte) (names []string, bodies map[string]string, hdrs map[string]*tar.Header) {
	t.Helper()
	bodies, hdrs = map[string]string{}, map[string]*tar.Header{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		names = append(names, h.Name)
		bodies[h.Name] = string(b)
		hdrs[h.Name] = h
	}
}

func dataVolume(uploadID int64) string { return backends.VolumeName(testInstance, uploadID) }

func calls(rt *backendstest.FakeRuntime) []string {
	rt.Lock()
	defer rt.Unlock()
	return slices.Clone(rt.Calls)
}

func hasSubsequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

func TestValidateDataArchive(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"valid", makeTar(t, 0, 0, tarEntry{Name: "a.txt", Body: "x"}, tarEntry{Name: "d/"}, tarEntry{Name: "d/l", Type: tar.TypeSymlink, Linkname: "../a.txt"}), ""},
		{"dot prefix", makeTar(t, 0, 0, tarEntry{Name: "./"}, tarEntry{Name: "./a.txt", Body: "x"}), ""},
		{"empty", makeTar(t, 0, 0), ""},
		{"absolute", makeTar(t, 0, 0, tarEntry{Name: "/abs"}), `archive entry "/abs" is outside /data`},
		{"parent", makeTar(t, 0, 0, tarEntry{Name: "../x"}), `archive entry "../x" is outside /data`},
		{"deep parent", makeTar(t, 0, 0, tarEntry{Name: "a/../../x"}), `archive entry "a/../../x" is outside /data`},
		{"hardlink", makeTar(t, 0, 0, tarEntry{Name: "h", Type: tar.TypeLink, Linkname: "a"}), `archive entry "h" is not a file, directory or symlink`},
		{"fifo", makeTar(t, 0, 0, tarEntry{Name: "f", Type: tar.TypeFifo}), `archive entry "f" is not a file, directory or symlink`},
		{"symlink abs", makeTar(t, 0, 0, tarEntry{Name: "l", Type: tar.TypeSymlink, Linkname: "/etc"}), `archive entry "l" links outside /data`},
		{"symlink up", makeTar(t, 0, 0, tarEntry{Name: "l", Type: tar.TypeSymlink, Linkname: "../../x"}), `archive entry "l" links outside /data`},
		{"symlink chain", makeTar(t, 0, 0, tarEntry{Name: "d", Type: tar.TypeSymlink, Linkname: "."}, tarEntry{Name: "d/s", Type: tar.TypeSymlink, Linkname: ".."}), `archive entry "d/s" passes through a symlink`},
		{"garbage", []byte(strings.Repeat("garbage!", 100)), "not a valid tar archive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := backends.ValidateDataArchive(bytes.NewReader(tc.data))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var ae *backends.ArchiveError
			if !errors.As(err, &ae) || ae.Msg != tc.wantErr {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestExportDataRewritesEntries(t *testing.T) {
	rt := backendstest.NewFakeRuntime()
	rt.Volumes[dataVolume(1)] = makeTar(t, 5, 6,
		tarEntry{Name: "data/"}, tarEntry{Name: "data/a.txt", Body: "A"},
		tarEntry{Name: "data/sub/"}, tarEntry{Name: "data/sub/b.txt", Body: "BB"})
	m := newManager(t, rt, backends.Limits{}, nil)
	var buf bytes.Buffer
	n, err := m.ExportData(context.Background(), spec(1, 1, 8080), &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(buf.Len()) {
		t.Errorf("count = %d, written %d", n, buf.Len())
	}
	names, bodies, _ := readTar(t, buf.Bytes())
	if !slices.Equal(names, []string{"a.txt", "sub/", "sub/b.txt"}) || bodies["a.txt"] != "A" || bodies["sub/b.txt"] != "BB" {
		t.Fatalf("names = %v bodies = %v", names, bodies)
	}
}

func TestExportDataStopsBackendsAndRemovesHelper(t *testing.T) {
	port := backendPort(t)
	rt := backendstest.NewFakeRuntime()
	m := newManager(t, rt, backends.Limits{}, nil)
	ensure(t, m, spec(1, 1, port)) // c1
	if _, err := m.ExportData(context.Background(), spec(1, 1, port), io.Discard); err != nil {
		t.Fatal(err)
	}
	if m.Running(1, 1) {
		t.Error("backend still running")
	}
	helper := backends.DataHelperName(testInstance, 1)
	got := calls(rt)
	if !hasSubsequence(got, []string{"stop c1", "create " + helper, "copyfrom c2 /data", "rmctr c2"}) {
		t.Fatalf("calls = %v", got)
	}
	if slices.Contains(got, "start c2") {
		t.Error("helper was started")
	}
	rt.Lock()
	last := rt.Created[len(rt.Created)-1]
	rt.Unlock()
	if last.Name != helper || last.Volume != dataVolume(1) || last.Labels["peek.role"] != "data-io" ||
		last.Labels["peek.managed"] != "true" || last.Labels["peek.instance"] != testInstance || last.Labels["peek.upload_id"] != "1" {
		t.Errorf("helper spec = %+v", last)
	}
	if left := rt.Containers(); len(left) != 0 {
		t.Errorf("containers left: %v", left)
	}
}

func TestImportDataReplacesVolume(t *testing.T) {
	port := backendPort(t)
	rt := backendstest.NewFakeRuntime()
	rt.DataUID, rt.DataGID = 65532, 65532
	m := newManager(t, rt, backends.Limits{}, nil)
	ensure(t, m, spec(1, 1, port)) // c1
	archive := makeTar(t, 1000, 1000, tarEntry{Name: "notes/"}, tarEntry{Name: "notes/hello.txt", Body: "hi"})
	if err := m.ImportData(context.Background(), spec(1, 1, port), bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	helper := backends.DataHelperName(testInstance, 1)
	vol := dataVolume(1)
	want := []string{"stop c1", "rmctr " + helper, "rmvol " + vol, "create " + helper, "copyfrom c2 /data", "copyto c2 /data", "rmctr c2"}
	if got := calls(rt); !hasSubsequence(got, want) {
		t.Fatalf("calls = %v, want subsequence %v", got, want)
	}
	rt.Lock()
	imported := rt.Imported[vol]
	rt.Unlock()
	names, bodies, hdrs := readTar(t, imported)
	if !slices.Equal(names, []string{"notes/", "notes/hello.txt"}) || bodies["notes/hello.txt"] != "hi" {
		t.Fatalf("imported names = %v bodies = %v", names, bodies)
	}
	for n, h := range hdrs {
		if h.Uid != 65532 || h.Gid != 65532 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s owner = %d:%d %q:%q", n, h.Uid, h.Gid, h.Uname, h.Gname)
		}
	}
}

// blockingWriter blocks its first Write until release is closed.
type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return len(p), nil
}

// holdExport starts an export of upload 1 that blocks in its first Write.
func holdExport(t *testing.T, m *backends.Manager, rt *backendstest.FakeRuntime) (*blockingWriter, chan error) {
	t.Helper()
	rt.Volumes[dataVolume(1)] = makeTar(t, 0, 0, tarEntry{Name: "data/"}, tarEntry{Name: "data/a", Body: "x"})
	bw := newBlockingWriter()
	done := make(chan error, 1)
	go func() {
		_, err := m.ExportData(context.Background(), spec(1, 1, 8080), bw)
		done <- err
	}()
	select {
	case <-bw.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("export never wrote")
	}
	return bw, done
}

func TestDataBusyBlocksEnsure(t *testing.T) {
	port := backendPort(t)
	rt := backendstest.NewFakeRuntime()
	m := newManager(t, rt, backends.Limits{}, nil)
	bw, done := holdExport(t, m, rt)

	if _, err := m.Ensure(context.Background(), spec(1, 1, port)); !errors.Is(err, backends.ErrDataBusy) {
		t.Errorf("Ensure while busy = %v", err)
	}
	if _, err := m.Ensure(context.Background(), spec(2, 1, port)); err != nil {
		t.Errorf("Ensure for another page = %v", err)
	}
	if _, err := m.ExportData(context.Background(), spec(1, 1, port), io.Discard); !errors.Is(err, backends.ErrDataBusy) {
		t.Errorf("second export = %v", err)
	}
	if err := m.ImportData(context.Background(), spec(1, 1, port), bytes.NewReader(makeTar(t, 0, 0))); !errors.Is(err, backends.ErrDataBusy) {
		t.Errorf("import while busy = %v", err)
	}
	close(bw.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), spec(1, 1, port)); err != nil {
		t.Errorf("Ensure after release = %v", err)
	}
}

func TestRemoveUploadDataWaitsForDataOp(t *testing.T) {
	rt := backendstest.NewFakeRuntime()
	m := newManager(t, rt, backends.Limits{}, nil)
	bw, done := holdExport(t, m, rt)

	removed := make(chan error, 1)
	go func() { removed <- m.RemoveUploadData(context.Background(), 1) }()
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-removed:
		t.Fatalf("RemoveUploadData finished while busy: %v", err)
	default:
	}
	if slices.Contains(calls(rt), "rmvol "+dataVolume(1)) {
		t.Fatal("rmvol ran during the data operation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() { cancelled <- m.RemoveUploadData(ctx, 1) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled RemoveUploadData = %v", err)
	}

	close(bw.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-removed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveUploadData did not finish")
	}
	got := calls(rt)
	if !hasSubsequence(got, []string{"rmctr c1", "rmvol " + dataVolume(1)}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestDataOpsAfterClose(t *testing.T) {
	rt := backendstest.NewFakeRuntime()
	m := newManager(t, rt, backends.Limits{}, nil)
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ExportData(context.Background(), spec(1, 1, 8080), io.Discard); err == nil {
		t.Error("ExportData after Close succeeded")
	}
	if err := m.ImportData(context.Background(), spec(1, 1, 8080), bytes.NewReader(makeTar(t, 0, 0))); err == nil {
		t.Error("ImportData after Close succeeded")
	}
	if err := m.RemoveUploadData(context.Background(), 1); err != nil {
		t.Errorf("RemoveUploadData after Close = %v", err)
	}
}

func TestDataOpErrors(t *testing.T) {
	port := backendPort(t)
	okTar := makeTar(t, 0, 0, tarEntry{Name: "a", Body: "x"})
	tests := []struct {
		name    string
		setup   func(rt *backendstest.FakeRuntime)
		run     func(m *backends.Manager) error
		wantErr string
	}{
		{"export pull failure", func(rt *backendstest.FakeRuntime) { rt.PullErr = errors.New("pull boom") },
			func(m *backends.Manager) error {
				_, err := m.ExportData(context.Background(), spec(1, 1, port), io.Discard)
				return err
			}, "pull boom"},
		{"import pull failure", func(rt *backendstest.FakeRuntime) { rt.PullErr = errors.New("pull boom") },
			func(m *backends.Manager) error {
				return m.ImportData(context.Background(), spec(1, 1, port), bytes.NewReader(okTar))
			}, "pull boom"},
		{"export create failure", func(rt *backendstest.FakeRuntime) {
			rt.Images[testImage] = true
			rt.CreateErr = errors.New("create boom")
		},
			func(m *backends.Manager) error {
				_, err := m.ExportData(context.Background(), spec(1, 1, port), io.Discard)
				return err
			}, "create boom"},
		{"export archive failure", func(rt *backendstest.FakeRuntime) {
			rt.Images[testImage] = true
			rt.ArchiveErr = errors.New("archive boom")
		},
			func(m *backends.Manager) error {
				_, err := m.ExportData(context.Background(), spec(1, 1, port), io.Discard)
				return err
			}, "archive boom"},
		{"import archive failure", func(rt *backendstest.FakeRuntime) {
			rt.Images[testImage] = true
			rt.ArchiveErr = errors.New("archive boom")
		},
			func(m *backends.Manager) error {
				return m.ImportData(context.Background(), spec(1, 1, port), bytes.NewReader(okTar))
			}, "archive boom"},
		{"export entry outside data", func(rt *backendstest.FakeRuntime) {
			rt.Images[testImage] = true
			rt.Volumes[dataVolume(1)] = makeTar(t, 0, 0, tarEntry{Name: "etc/passwd", Body: "x"})
		},
			func(m *backends.Manager) error {
				_, err := m.ExportData(context.Background(), spec(1, 1, port), io.Discard)
				return err
			}, `unexpected archive entry "etc/passwd"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := backendstest.NewFakeRuntime()
			tc.setup(rt)
			m := newManager(t, rt, backends.Limits{}, nil)
			err := tc.run(m)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if left := rt.Containers(); len(left) != 0 {
				t.Errorf("containers left: %v", left)
			}
			rt.Lock()
			rt.PullErr, rt.CreateErr, rt.ArchiveErr = nil, nil, nil
			rt.Unlock()
			if _, err := m.Ensure(context.Background(), spec(1, 1, port)); err != nil {
				t.Errorf("Ensure after failed op = %v", err)
			}
		})
	}
}

// recreatingRuntime puts a volume with a wrong root entry back after RemoveVolume,
// as an engine whose fresh volume does not look like /data would.
type recreatingRuntime struct {
	*backendstest.FakeRuntime
	tar []byte
}

func (r *recreatingRuntime) RemoveVolume(ctx context.Context, name string) error {
	if err := r.FakeRuntime.RemoveVolume(ctx, name); err != nil {
		return err
	}
	r.Lock()
	r.Volumes[name] = r.tar
	r.Unlock()
	return nil
}

func TestDataOpErrorsImportVolumeRoot(t *testing.T) {
	fake := backendstest.NewFakeRuntime()
	fake.Images[testImage] = true
	rt := &recreatingRuntime{FakeRuntime: fake, tar: makeTar(t, 0, 0, tarEntry{Name: "other/"})}
	m := newManager(t, rt, backends.Limits{}, nil)
	err := m.ImportData(context.Background(), spec(1, 1, 8080), bytes.NewReader(makeTar(t, 0, 0)))
	if err == nil || !strings.Contains(err.Error(), `unexpected archive entry "other/"`) {
		t.Fatalf("err = %v", err)
	}
	if left := fake.Containers(); len(left) != 0 {
		t.Errorf("containers left: %v", left)
	}
	if _, err := m.Ensure(context.Background(), spec(1, 1, backendPort(t))); err != nil {
		t.Errorf("Ensure after failed import = %v", err)
	}
}
