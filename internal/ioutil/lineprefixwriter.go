package ioutil

import (
	"bytes"
	"io"
)

// LinePrefixer is a writer that prefixes each line written to it with a prefix.
type LinePrefixer struct {
	W      io.Writer
	buf    []byte
	Prefix []byte
}

func (l *LinePrefixer) Write(p []byte) (n int, err error) {
	n = len(p)
	l.buf = append(l.buf, p...)
	if !bytes.Contains(p, []byte{'\n'}) { // no newlines in p, short-circuit out
		return
	}
	bufOrig := l.buf
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if _, err := l.W.Write(l.Prefix); err != nil {
			return 0, err
		}
		if _, err := l.W.Write(l.buf[:i+1]); err != nil {
			return 0, err
		}
		l.buf = l.buf[i+1:]
	}
	l.buf = append(bufOrig[:0], l.buf...)
	return
}

func (l *LinePrefixer) Close() error {
	if len(l.buf) > 0 {
		if _, err := l.W.Write(l.Prefix); err != nil {
			return err
		}
		if _, err := l.W.Write(l.buf); err != nil {
			return err
		}
	}
	return nil
}
