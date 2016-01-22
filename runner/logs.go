package runner

import (
	"io"
	"os"
	"path/filepath"

	"github.com/Sirupsen/logrus"
)

// LogCapturer is an interface for providing
// writers to a logging backend.
type LogCapturer interface {
	Stdout() io.Writer
	Stderr() io.Writer
	Close() error
}

type consoleLogger struct{}

func (consoleLogger) Stdout() io.Writer {
	return os.Stdout
}

func (consoleLogger) Stderr() io.Writer {
	return os.Stderr
}

func (consoleLogger) Close() error {
	return nil
}

// NewConsoleLogCapturer creates a new log capturer
// which uses the console as a backend.
func NewConsoleLogCapturer() LogCapturer {
	return consoleLogger{}
}

type fileLogger struct {
	stdout io.WriteCloser
	stderr io.WriteCloser
}

// NewFileLogCapturer uses files as a logging backend.
// Stdout and Stderr will be written to separate files
// with suffixes "-stdout" and "-stderr".
func NewFileLogCapturer(basename string) (LogCapturer, error) {
	if err := os.MkdirAll(filepath.Dir(basename), 0755); err != nil {
		return nil, err
	}
	outF, err := os.Create(basename + "-stdout")
	if err != nil {
		return nil, err
	}
	errF, err := os.Create(basename + "-stderr")
	if err != nil {
		return nil, err
	}
	return &fileLogger{
		stdout: outF,
		stderr: errF,
	}, nil
}

func (fl *fileLogger) Stdout() io.Writer {
	return fl.stdout
}

func (fl *fileLogger) Stderr() io.Writer {
	return fl.stderr
}

func (fl *fileLogger) Close() error {
	if err := fl.stdout.Close(); err != nil {
		logrus.Errorf("Error closing stdout: %v", err)
	}
	if err := fl.stderr.Close(); err != nil {
		logrus.Errorf("Error closing stderr: %v", err)
	}
	return nil
}
