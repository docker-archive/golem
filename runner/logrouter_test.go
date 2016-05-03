package runner

import (
	"bytes"
	"io"
	"testing"
)

func assertWrite(t *testing.T, w io.Writer, s string) {
	if _, err := w.Write([]byte(s + "\n")); err != nil {
		t.Fatal(err)
	}
}

func checkBuffer(t *testing.T, buf *bytes.Buffer, content []byte) {
	if bytes.Compare(buf.Bytes(), content) != 0 {
		t.Fatalf("Unexpected buffer content\n\tExpected:\n%q\n\tActual:\n%q", content, buf.Bytes())
	}
}

func TestAddWriter(t *testing.T) {
	b1 := bytes.NewBuffer(nil)
	b2 := bytes.NewBuffer(nil)
	b3 := bytes.NewBuffer(nil)
	mw := NewLogMultiWriter(b1)

	assertWrite(t, mw, "First line")

	mw.AddWriter(b2)

	assertWrite(t, mw, "Second line")

	// Additional add should be no-op
	mw.AddWriter(b2)

	mw.AddWriter(b3)

	assertWrite(t, mw, "Third line")

	mw.RemoveWriter(b2)

	assertWrite(t, mw, "Fourth line")

	expected1 := []byte(`First line
Second line
Third line
Fourth line
`)

	expected2 := []byte("Second line\nThird line\n")
	expected3 := []byte("Third line\nFourth line\n")

	checkBuffer(t, b1, expected1)
	checkBuffer(t, b2, expected2)
	checkBuffer(t, b3, expected3)

}

type bufferLogger struct {
	stderr *bytes.Buffer
	stdout *bytes.Buffer
}

func (bl *bufferLogger) Stdout() io.Writer {
	return bl.stdout
}

func (bl *bufferLogger) Stderr() io.Writer {
	return bl.stderr
}

func (bl *bufferLogger) Close() error {
	return nil
}

func newBufferLogger() *bufferLogger {
	return &bufferLogger{
		stderr: bytes.NewBuffer(nil),
		stdout: bytes.NewBuffer(nil),
	}
}

func TestLogTapper(t *testing.T) {
	c := newBufferLogger()
	tapped := newLogTapper(c)

	assertWrite(t, tapped.Stdout(), "First line")

	r1 := tapped.TapStdout()
	b1 := bytes.NewBuffer(nil)
	done1 := make(chan error)
	go func() {
		defer close(done1)
		_, err := io.Copy(b1, r1)
		if err != nil {
			done1 <- err
		}
	}()

	assertWrite(t, tapped.Stdout(), "Second line")

	r2 := tapped.TapStdout()
	b2 := bytes.NewBuffer(nil)
	done2 := make(chan error)
	go func() {
		defer close(done2)
		_, err := io.Copy(b2, r2)
		if err != nil {
			done2 <- err
		}
	}()

	assertWrite(t, tapped.Stdout(), "Third line")

	if err := r2.Close(); err != nil {
		t.Fatal(err)
	}

	<-done2

	assertWrite(t, tapped.Stdout(), "Fourth line")

	if err := tapped.Close(); err != nil {
		t.Fatal(err)
	}

	<-done1

	expectedAll := []byte(`First line
Second line
Third line
Fourth line
`)

	expected1 := []byte("Second line\nThird line\nFourth line\n")
	expected2 := []byte("Third line\n")

	checkBuffer(t, c.stdout, expectedAll)
	checkBuffer(t, b1, expected1)
	checkBuffer(t, b2, expected2)

}
