package main

import (
	"archive/zip"
	"fmt"
	"io"
)

// ---------------------------------------------------------------------------
// Zip helpers
// ---------------------------------------------------------------------------

func zipIndex(r *zip.Reader) map[string]*zip.File {
	m := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		m[f.Name] = f
	}
	return m
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func writeZipEntry(w *zip.Writer, name string, data []byte) error {
	fw, err := w.Create(name)
	if err != nil {
		return err
	}
	_, err = fw.Write(data)
	return err
}

// checkedZipWriter wraps a *zip.Writer and records the first error
// encountered. Subsequent writes become no-ops after the first failure.
type checkedZipWriter struct {
	w   *zip.Writer
	err error
}

func (cw *checkedZipWriter) write(name string, data []byte) {
	if cw.err != nil {
		return
	}
	cw.err = writeZipEntry(cw.w, name, data)
}

// startEpubZip creates the mandatory first entries in an epub zip: the
// uncompressed mimetype entry and META-INF/container.xml. It returns a
// checkedZipWriter for subsequent writes.
func startEpubZip(out *zip.Writer) (*checkedZipWriter, error) {
	mh := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	mw, err := out.CreateHeader(mh)
	if err != nil {
		return nil, fmt.Errorf("writing mimetype: %w", err)
	}
	if _, err := mw.Write([]byte("application/epub+zip")); err != nil {
		return nil, fmt.Errorf("writing mimetype: %w", err)
	}
	cw := &checkedZipWriter{w: out}
	cw.write("META-INF/container.xml", []byte(containerXML))
	return cw, cw.err
}
