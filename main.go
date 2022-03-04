package main

import (
	"context"
	"crypto/md5" // nolint: gosec
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// exitCodeOK indicates that the application successfully finishes the task.
	exitCodeOK = 0
	// exitCodeError indicates that there is an error occurred in the process.
	exitCodeError = 1
	// exitCodeCanceled indicates that the process is canceled, probably because of a ^C.
	exitCodeCanceled = 2
	// exitCodeBadArgs indicates that provided arguments are invalid.
	exitCodeBadArgs = 3

	// defaultNumWorkers is the default value for number of workers.
	defaultNumWorkers = 10
	// defaultTimeout is the default timeout for requesting an url.
	defaultTimeout = 30 * time.Second
	// defaultUserAgent is the default user agent to disguise.
	defaultUserAgent = `Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/98.0.4758.102 Safari/537.36 Edg/98.0.1108.56`

	// Limitation for number of workers to avoid resource saturation.
	maxNumWorkers = 24
)

var (
	// argNumWorkers is the number of workers for hashing urls. Default to defaultNumWorkers.
	argNumWorkers = defaultNumWorkers

	stdOut io.Writer = os.Stdout
	stdErr io.Writer = os.Stderr

	// httpClient is the http client for requesting an url.
	httpClient = http.DefaultClient

	// outputMu ensures only one message is written to output.
	outputMu = sync.Mutex{}
)

// init is for registering all the arguments.
// nolint: gochecknoinits
func init() {
	flag.IntVar(&argNumWorkers, "parallel", defaultNumWorkers, "-parallel 24")
}

func main() {
	os.Exit(runMain())
}

func runMain() int {
	flag.Parse()

	if argNumWorkers > maxNumWorkers {
		_, _ = fmt.Fprintf(stdErr, "maximum workers is %d\n", maxNumWorkers)

		return exitCodeBadArgs
	}

	urls, err := parseURLs(flag.Args())
	if err != nil {
		_, _ = fmt.Fprintf(stdErr, "%s\n", err)

		return exitCodeBadArgs
	}

	ctx, cancel := context.WithCancel(context.Background())

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	defer func() {
		close(sigs)
		cancel()
	}()

	go func() {
		<-sigs
		cancel()
	}()

	return hashURLs(ctx, urls, argNumWorkers, defaultTimeout, stdOut, stdErr)
}

func parseURLs(urls []string) ([]url.URL, error) {
	result := make([]url.URL, 0, len(urls))

	for _, str := range urls {
		if !strings.Contains(str, "://") {
			str = "https://" + str
		}

		u, err := url.Parse(str)
		if err != nil {
			return nil, err
		}

		if u.Host == "" {
			return nil, fmt.Errorf("parse %q: missing hostname", str)
		}

		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("parse %q: unsupported scheme %q", str, u.Scheme)
		}

		result = append(result, *u)
	}

	return result, nil
}

// hashStream reads data from the reader and writes to the hash to save memory allocation for big data.
func hashStream(r io.Reader) (string, error) {
	// md5 is explicitly requested for.
	// nolint: gosec
	hash := md5.New()

	if r != nil {
		buf := make([]byte, 512)

		for {
			received, err := r.Read(buf)
			if received > 0 {
				_, err := hash.Write(buf[:received])
				mustNoError(err) // This should not happen.
			}

			if err != nil {
				if !errors.Is(err, io.EOF) {
					return "", fmt.Errorf("could not read from source: %w", err)
				}

				break
			}
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashURL(ctx context.Context, url url.URL) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url.String(), nil)
	mustNoError(err) // This should not happen because all the params are validated.

	req.Header.Add("User-Agent", defaultUserAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not request: %w", err)
	}

	defer resp.Body.Close() // nolint: errcheck

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request got unexpected code %d", resp.StatusCode)
	}

	return hashStream(resp.Body)
}

func hashURLWorker(ctx context.Context, ch <-chan url.URL, workerTimeout time.Duration, out io.Writer, outErr io.Writer) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case u, ok := <-ch:
			if !ok {
				return nil
			}

			ctx, cancelHashCtx := context.WithTimeout(ctx, workerTimeout)
			hash, err := hashURL(ctx, u)

			cancelHashCtx()

			if err != nil {
				if !errors.Is(err, context.Canceled) {
					writeOut(outErr, u, err.Error())
				}

				return err
			}

			writeOut(out, u, hash)
		}
	}
}

func hashURLs(ctx context.Context, urls []url.URL, numWorkers int, workerTimeout time.Duration, out io.Writer, outErr io.Writer) int {
	numURLs := len(urls)
	if numURLs == 0 {
		return exitCodeOK
	}

	// Avoid waste of resource.
	if numURLs < numWorkers {
		numWorkers = numURLs
	}

	code := int64(exitCodeOK)
	wg := sync.WaitGroup{}
	ch := make(chan url.URL, len(urls))

	wg.Add(numWorkers)

	// Start the workers.
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()

			if err := hashURLWorker(ctx, ch, workerTimeout, out, outErr); err != nil {
				errToCode(&code, err)
			}
		}()
	}

	// Start the producer to send urls to workers.
	go func() {
		for _, u := range urls {
			ch <- u
		}

		close(ch)
	}()

	wg.Wait()

	return int(code)
}

func writeOut(w io.Writer, url url.URL, message string) {
	outputMu.Lock()
	defer outputMu.Unlock()

	_, err := fmt.Fprintf(w, "%s %s\n", url.String(), message)
	mustNoError(err)
}

func mustNoError(err error) {
	if err != nil {
		panic(err)
	}
}

func errToCode(code *int64, err error) {
	if err == nil {
		return
	}

	if c := atomic.LoadInt64(code); c == exitCodeError {
		return
	}

	if errors.Is(err, context.Canceled) {
		atomic.StoreInt64(code, exitCodeCanceled)
	} else {
		atomic.StoreInt64(code, exitCodeError)
	}
}
