package runner

import (
	"bufio"
	"errors"
	"io"
	"path/filepath"
	"sync"

	"github.com/Sirupsen/logrus"
)

// MultiWriter defines a type which can write to multiple
// writers and allows adding and removing sinks.
type MultiWriter interface {
	io.Writer
	AddWriter(io.Writer)
	RemoveWriter(io.Writer)
}

// LogForwarder defines a type which can forward named
// log streams.
type LogForwarder interface {
	StartForward(string, io.ReadCloser) error
	StopForward(string) error
}

type logMultiWriter struct {
	sink        io.Writer
	writersLock sync.Mutex
	writers     map[io.Writer]struct{}
}

// NewLogMultiWriter creates a MultiWriter with a constant sink
// which cannot be altered.
func NewLogMultiWriter(w io.Writer) MultiWriter {
	return &logMultiWriter{
		sink:    w,
		writers: map[io.Writer]struct{}{},
	}
}

func (lmw *logMultiWriter) Write(b []byte) (n int, err error) {
	n, err = lmw.sink.Write(b)
	if err != nil {
		return
	}
	if n != len(b) {
		err = io.ErrShortWrite
		return
	}

	lmw.writersLock.Lock()
	defer lmw.writersLock.Unlock()

	for w := range lmw.writers {
		wN, wErr := w.Write(b)
		if wErr != nil {
			logrus.Debugf("Error writing to output stream, removing: %#v", wErr)
			delete(lmw.writers, w)
			continue
		}
		if wN != n {
			// TODO(dmcgowan): Keep writing until wN == n?
			logrus.Debugf("Error short write, removing")
			delete(lmw.writers, w)
		}
	}

	return
}

func (lmw *logMultiWriter) AddWriter(w io.Writer) {
	lmw.writersLock.Lock()
	defer lmw.writersLock.Unlock()
	lmw.writers[w] = struct{}{}
}

func (lmw *logMultiWriter) RemoveWriter(w io.Writer) {
	lmw.writersLock.Lock()
	defer lmw.writersLock.Unlock()
	delete(lmw.writers, w)

}

type logTapper struct {
	stderr MultiWriter
	stdout MultiWriter
	closer io.Closer

	l    sync.Mutex
	taps map[*logTap]MultiWriter
}

type logTap struct {
	mw     MultiWriter
	r      io.Reader
	wp     *io.PipeWriter
	tapper *logTapper
}

func newLogTapper(sink LogCapturer) *logTapper {
	return &logTapper{
		stdout: NewLogMultiWriter(sink.Stdout()),
		stderr: NewLogMultiWriter(sink.Stderr()),
		closer: sink,
		taps:   map[*logTap]MultiWriter{},
	}
}

func (lr *logTapper) Stdout() io.Writer {
	return lr.stdout
}

func (lr *logTapper) Stderr() io.Writer {
	return lr.stderr
}

func (lr *logTapper) TapStdout() io.ReadCloser {
	return lr.addTap(lr.stdout)
}

func (lr *logTapper) TapStderr() io.ReadCloser {
	return lr.addTap(lr.stderr)
}

func (lr *logTapper) addTap(mw MultiWriter) io.ReadCloser {
	r, w := io.Pipe()
	mw.AddWriter(w)
	t := &logTap{
		r:      bufio.NewReader(r),
		wp:     w,
		tapper: lr,
	}

	lr.l.Lock()
	defer lr.l.Unlock()

	lr.taps[t] = mw

	return t
}

func (lr *logTapper) removeTap(t *logTap) error {
	lr.l.Lock()
	defer lr.l.Unlock()
	if mw, ok := lr.taps[t]; ok {
		delete(lr.taps, t)
		mw.RemoveWriter(t.wp)
		return t.wp.Close()
	}

	return nil
}

func (lr *logTapper) removeAllTaps() {
	lr.l.Lock()
	defer lr.l.Unlock()
	for t, mw := range lr.taps {
		mw.RemoveWriter(t.wp)
		if err := t.wp.Close(); err != nil {
			logrus.Debugf("error closing writer tap: %v", err)
		}
	}
	lr.taps = map[*logTap]MultiWriter{}
}

func (lr *logTapper) Close() error {
	lr.removeAllTaps()
	return lr.closer.Close()
}

func (t *logTap) Read(b []byte) (n int, err error) {
	n, err = t.r.Read(b)
	if err == io.ErrClosedPipe {
		err = io.EOF
	}
	return
}

func (t *logTap) Close() error {
	return t.tapper.removeTap(t)
}

type nilLogger struct{}

func (nilLogger) Write(b []byte) (int, error) {
	return len(b), nil
}

func (nilLogger) Stdout() io.Writer {
	return nilLogger{}
}

func (nilLogger) Stderr() io.Writer {
	return nilLogger{}
}

func (nilLogger) Close() error {
	return nil
}

// LogRouter manages log streams as well as the
// creation and routing of those streams.
type LogRouter struct {
	logDir string

	l          sync.Mutex
	logStreams map[string]*logTapper
	forwards   []LogForwarder

	forwardChan chan LogForwarder
	streamChan  chan string
	closeChan   chan struct{}
}

// NewLogRouter creates a new LogRouter with a directory
// as a default log sink for all created log streams. If
// the log directory is empty, log streams will not be
// saved by the router.
func NewLogRouter(logDirectory string) *LogRouter {
	// Create channels
	lr := &LogRouter{
		logDir:     logDirectory,
		logStreams: map[string]*logTapper{},
		forwards:   []LogForwarder{},

		forwardChan: make(chan LogForwarder),
		streamChan:  make(chan string),
		closeChan:   make(chan struct{}),
	}
	go lr.route()
	return lr
}

func forwardStream(f LogForwarder, name string, t *logTapper) {
	forwardName := name + "-stdout"
	if err := f.StartForward(forwardName, t.TapStdout()); err != nil {
		logrus.Errorf("unable to start forwarder %s: %v", forwardName, err)
	}
	forwardName = name + "-stderr"
	if err := f.StartForward(forwardName, t.TapStderr()); err != nil {
		logrus.Errorf("unable to start forwarder %s: %v", forwardName, err)
	}
	// TODO: Handle errors to ensure caller does not attempt to stop
}

func (lr *LogRouter) route() {
	defer logrus.Debugf("Log router completed")
	for {
		select {
		case f := <-lr.forwardChan:
			lr.l.Lock()
			for name, t := range lr.logStreams {
				forwardStream(f, name, t)
			}
			lr.forwards = append(lr.forwards, f)
			lr.l.Unlock()
		case name := <-lr.streamChan:
			lr.l.Lock()
			t, ok := lr.logStreams[name]
			if ok {
				for _, f := range lr.forwards {
					forwardStream(f, name, t)
				}
			}
			lr.l.Unlock()
		case <-lr.closeChan:
			lr.l.Lock()
			for name := range lr.logStreams {
				for _, f := range lr.forwards {
					forwardName := name + "-stdout"
					if err := f.StopForward(forwardName); err != nil {
						logrus.Errorf("error stopping forward %s: %v", forwardName, err)
					}
					forwardName = name + "-stderr"
					if err := f.StopForward(forwardName); err != nil {
						logrus.Errorf("error stopping forward %s: %v", forwardName, err)
					}
				}
			}
			lr.l.Unlock()
			return
		}
	}
}

// RouteLogCapturer creates a new log stream with the provided name
// returning a log capturer and any error while creating the stream.
func (lr *LogRouter) RouteLogCapturer(name string) (capturer LogCapturer, err error) {
	defer func() {
		if err == nil {
			lr.streamChan <- name
		}
	}()
	lr.l.Lock()
	defer lr.l.Unlock()

	tapped, ok := lr.logStreams[name]
	if ok {
		return tapped, nil
	}

	if lr.streamChan == nil {
		return nil, errors.New("cannot create log capturer on closed router")
	}

	if lr.logDir == "" {
		capturer = nilLogger{}
	} else {
		basename := filepath.Join("/var/log/docker", name)
		capturer, err = NewFileLogCapturer(basename)
		if err != nil {
			return
		}
	}

	tapped = newLogTapper(capturer)

	lr.logStreams[name] = tapped

	return tapped, nil
}

func copyTap(name string, w io.Writer, r io.ReadCloser) {
	defer r.Close()
	if _, err := io.Copy(w, r); err != nil {
		logrus.Errorf("Capture router copy failed for %s: %v", name, err)
	}
	logrus.Debugf("Done copying tap %s", name)
}

// AddCapturer adds a log capturer to an existing log stream as
// an output sink. Only new data on the log stream will get sent
// to the log capturer.
func (lr *LogRouter) AddCapturer(name string, c LogCapturer) error {
	lr.l.Lock()
	defer lr.l.Unlock()

	tapped, ok := lr.logStreams[name]
	if !ok {
		return errors.New("log stream does not exist")
	}

	go copyTap(name, c.Stdout(), tapped.TapStdout())
	go copyTap(name, c.Stderr(), tapped.TapStderr())

	return nil
}

// AddForwarder adds a forwarder for all log streams. All existing
// log streams will begin to be forwarded to the provided log forwarder
// in addition to existing forwarders. Only new data on the streams
// will get forwarded. This operation does not lock the log streams, not
// guaranteeing that data written at the same time as the forwarder
// being added will get forwarded.
func (lr *LogRouter) AddForwarder(forwarder LogForwarder) (err error) {
	defer func() {
		if err == nil {
			lr.forwardChan <- forwarder
		}
	}()
	lr.l.Lock()
	defer lr.l.Unlock()

	if lr.forwardChan == nil {
		return errors.New("router shut down")
	}

	return nil
}

// Shutdown closes the log router by detaching all sinks and forwards
// and closing all streams.
func (lr *LogRouter) Shutdown() {
	lr.l.Lock()
	defer lr.l.Unlock()

	lr.forwardChan = nil
	lr.streamChan = nil

	close(lr.closeChan)
}
