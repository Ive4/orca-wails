package core

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type coreOutput struct {
	stdin                      *os.File
	stdoutReader, stdoutWriter *os.File
	stderrReader, stderrWriter *os.File
	stdoutLog, stderrLog       *os.File
}

func prepareOutput(dir string) (*coreOutput, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("could not create the core log directory: %w", err)
	}
	var p coreOutput
	ok := false
	defer func() {
		if !ok {
			p.closeChildEnds()
			p.closeParentEnds()
		}
	}()
	var err error
	if p.stdoutLog, err = openLog(dir, "core.stdout.log"); err != nil {
		return nil, err
	}
	if p.stderrLog, err = openLog(dir, "core.stderr.log"); err != nil {
		return nil, err
	}
	if p.stdin, err = os.Open(os.DevNull); err != nil {
		return nil, fmt.Errorf("could not open null core stdin: %w", err)
	}
	if p.stdoutReader, p.stdoutWriter, err = newOutputPipe(); err != nil {
		return nil, fmt.Errorf("could not open core stdout pipe: %w", err)
	}
	if p.stderrReader, p.stderrWriter, err = newOutputPipe(); err != nil {
		return nil, fmt.Errorf("could not open core stderr pipe: %w", err)
	}
	ok = true
	return &p, nil
}

func (p *coreOutput) closeChildEnds() {
	closeFiles(p.stdin, p.stdoutWriter, p.stderrWriter)
}

func (p *coreOutput) closeParentEnds() {
	closeFiles(p.stdoutReader, p.stderrReader, p.stdoutLog, p.stderrLog)
}

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func (p *coreOutput) drain(events chan<- readinessEvent) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		var readers sync.WaitGroup
		readers.Go(func() {
			defer closeFiles(p.stdoutReader, p.stdoutLog)
			readStdout(p.stdoutReader, p.stdoutLog, events)
		})
		readers.Go(func() {
			defer closeFiles(p.stderrReader, p.stderrLog)
			readStderr(p.stderrReader, p.stderrLog)
		})
		readers.Wait()
		close(done)
	}()
	return done
}

func openLog(dir, name string) (*os.File, error) {
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err == nil && info.Size() > 10*1024*1024 {
		previous := path + ".previous"
		if err := os.Remove(previous); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("could not remove previous core log: %w", err)
		}
		if err := os.Rename(path, previous); err != nil {
			return nil, fmt.Errorf("could not rotate core log: %w", err)
		}
	}
	log, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("could not open core log: %w", err)
	}
	return log, nil
}

func readStdout(reader io.Reader, log io.Writer, events chan<- readinessEvent) {
	sent := false
	readRecords(reader, 1024*1024, false, func(line []byte) {
		if sent {
			return
		}
		if event := decodeReadiness(line); event != nil {
			sent = true
			fmt.Fprintln(log, "Core readiness received; pairing credentials omitted.")
			events <- event
		}
	}, func() {
		fmt.Fprintln(log, "Oversized core stdout record omitted.")
	})
}

func readStderr(reader io.Reader, log io.Writer) {
	readRecords(reader, 64*1024, true, func(line []byte) {
		fmt.Fprintln(log, redactDiagnostic(string(line)))
	}, func() {
		fmt.Fprintln(log, "[oversized core diagnostic omitted]")
	})
}

func readRecords(reader io.Reader, limit int, includeLast bool, record func([]byte), oversized func()) {
	buffer := bufio.NewReaderSize(reader, 8192)
	var line []byte
	var overflow bool
	for {
		part, err := buffer.ReadSlice('\n')
		if err == nil {
			part = part[:len(part)-1]
		}
		if !overflow {
			if len(line)+len(part) > limit {
				overflow = true
				oversized()
			} else {
				line = append(line, part...)
			}
		}
		switch err {
		case nil:
			if !overflow {
				record(line)
			}
			line, overflow = line[:0], false
		case bufio.ErrBufferFull:
			continue
		default:
			if err == io.EOF && includeLast && !overflow && len(line) > 0 {
				record(line)
			}
			return
		}
	}
}

var diagnosticURL = regexp.MustCompile(`(?i)(?:https?|wss?)://[^\s\p{Z}\x{0085}"'<>]*`)

func redactDiagnostic(line string) string {
	line = strings.ToValidUTF8(line, "\uFFFD")
	lower := strings.ToLower(line)
	for _, marker := range []string{
		"pairing", "offer", "orca://", "token", "secret", "password",
		"authorization", "api_key", "apikey", "credential",
	} {
		if strings.Contains(lower, marker) {
			return "[sensitive core diagnostic omitted]"
		}
	}
	return diagnosticURL.ReplaceAllStringFunc(line, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "[URL omitted]"
		}
		u.User = nil
		u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
		u.ForceQuery = false
		return u.String()
	})
}
