package runner

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"

	"github.com/Sirupsen/logrus"
	"github.com/docker/libchan"
	"github.com/docker/libchan/spdy"
)

type tapStreamMessage struct {
	Name   string
	Stdout bool
	W      io.Writer
	Err    libchan.Sender
	Done   libchan.Receiver
}

type errStreamMessage struct {
	Message string
}

func TapServer(l net.Listener, lr *LogRouter) {
	for {
		c, err := l.Accept()
		if err != nil {
			if err != io.EOF {
				logrus.Errorf("Listen error: %#v", err)
			}
			return
		}

		p, err := spdy.NewSpdyStreamProvider(c, true)
		if err != nil {
			logrus.Errorf("Error creating stream provider: %#v", err)
			continue
		}
		t := spdy.NewTransport(p)
		go func() {
			r, err := t.WaitReceiveChannel()
			if err != nil {
				logrus.Errorf("Error receiving channel, ending libchan transport: %s", err)
				return
			}
			for {
				var tm tapStreamMessage
				if err := r.Receive(&tm); err != nil {
					if err != io.EOF {
						logrus.Errorf("Error receiving message, ending libchan transport: %s", err)
					}
					return
				}

				ts, ok := lr.logStreams[tm.Name]
				if !ok {
					tm.Err.Send(errStreamMessage{Message: "missing named stream"})
					// TODO: Check send error
					tm.Err.Close()
					continue
				}

				var tap io.ReadCloser

				if tm.Stdout {
					tap = ts.TapStdout()
				} else {
					tap = ts.TapStderr()
				}

				go func() {
					defer tm.Err.Close()
					_, err := io.Copy(tm.W, tap)
					if err != nil {
						logrus.Errorf("Error copying tap: %v", err)
						tm.Err.Send(errStreamMessage{Message: err.Error()})
					}
				}()

				go func() {
					defer tap.Close()
					var s struct{}
					if err := tm.Done.Receive(&s); err != nil && err != io.EOF {
						logrus.Errorf("Error reading from done: %s", err)
					}
				}()
			}
		}()
	}
}

func TapClient(client net.Conn, name string, stderr bool) error {
	provider, err := spdy.NewSpdyStreamProvider(client, false)
	if err != nil {
		return err
	}

	transport := spdy.NewTransport(provider)
	sender, err := transport.NewSendChannel()
	if err != nil {
		return err
	}
	defer sender.Close()

	remoteDone, done := libchan.Pipe()
	errPipe, remoteErrPipe := libchan.Pipe()

	sm := tapStreamMessage{
		Done:   remoteDone,
		Err:    remoteErrPipe,
		Name:   name,
		Stdout: !stderr,
		W:      os.Stdout,
	}

	if err := sender.Send(&sm); err != nil {
		return err
	}

	signalChan := make(chan os.Signal)
	signal.Notify(signalChan, os.Interrupt, os.Kill)
	go func() {
		<-signalChan
		if err := done.Close(); err != nil {
			logrus.Errorf("Error closing done channel")
		}
	}()

	var em errStreamMessage
	if err := errPipe.Receive(&em); err != nil && err != io.EOF {
		return err
	}

	if em.Message != "" {
		return fmt.Errorf("remote error: %s", em.Message)
	}

	return nil
}
