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
	defaultUserAgent = `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/99.0.4844.51 Safari/537.36`

	// Limitation for number of workers to avoid resource saturation.
	maxNumWorkers = 24
)

var (
	// argNumWorkers is the number of workers for hashing urls. Default to defaultNumWorkers.
	argNumWorkers = defaultNumWorkers

	// The outputs to print out results.
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

// runMain runs the application and validate arguments.
//
// Notes:
// 	- If -parallel is omitted, the number of workers is defaultNumWorkers. Otherwise, a number in between 1 and maxNumWorkers is accepted.
// 	- The url does not need a scheme, in case of omitting, "https" is automatically appended to the url.
func runMain() int {
	flag.Parse()

	if argNumWorkers < 1 {
		_, _ = fmt.Fprintf(stdErr, "number of workers must be greater than 0\n")

		return exitCodeBadArgs
	} else if argNumWorkers > maxNumWorkers {
		_, _ = fmt.Fprintf(stdErr, "maximum workers is %d\n", maxNumWorkers)

		return exitCodeBadArgs
	}

	urls, err := parseURLs(flag.Args())
	if err != nil {
		_, _ = fmt.Fprintf(stdErr, "%s\n", err)

		return exitCodeBadArgs
	}

	// Graceful shutdown does not bring any values in this application. It is just for demonstration and fun.
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

// parseURLs parses the urls string and returns a list of url.URL. If the scheme is omitted, "https" is appended.
// The function will check for missing hostname and unsupported schemes.
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

// hashURL creates a hash for the given url by hashing the content using md5.
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

// hashURLWorker reads and hash the urls then sends to the outputs. The worker stops when the context is done or no url to process (channel is closed).
func hashURLWorker(ctx context.Context, ch <-chan url.URL, workerTimeout time.Duration, out io.Writer, outErr io.Writer) error {
	for {
		select {
		// Context is done, probably because of canceling or timing out.
		case <-ctx.Done():
			return ctx.Err()

		case u, ok := <-ch:
			if !ok {
				// Stop the worker if channel is closed.
				return nil
			}

			ctx, cancelHashCtx := context.WithTimeout(ctx, workerTimeout)
			hash, err := hashURL(ctx, u)

			cancelHashCtx()

			if err != nil {
				// If the context is intentionally canceled, we do not consider that as an error.
				if !errors.Is(err, context.Canceled) {
					writeOut(outErr, u, err.Error())
				}

				return err
			}

			writeOut(out, u, hash)
		}
	}
}

// hashURLProducer sends the urls asynchronously.
func hashURLProducer(urls []url.URL) <-chan url.URL {
	ch := make(chan url.URL, len(urls))

	go func() {
		for _, u := range urls {
			ch <- u
		}

		close(ch)
	}()

	return ch
}

// hashURLs hashes the urls using multiple workers following the producer/worker pattern and sends result to the outputs.
// The returned status code indicates the result of the whole process.
func hashURLs(ctx context.Context, urls []url.URL, numWorkers int, workerTimeout time.Duration, out io.Writer, outErr io.Writer) int {
	numURLs := len(urls)
	if numURLs == 0 {
		// Nothing to do.
		return exitCodeOK
	}

	// Avoid waste of resource.
	if numURLs < numWorkers {
		numWorkers = numURLs
	}

	code := int64(exitCodeOK)
	wg := sync.WaitGroup{}

	// Start the producer to send urls to workers.
	ch := hashURLProducer(urls)

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

	wg.Wait()

	return int(code)
}

// writeOut ensures only worker could write to either out or outErr at a time. This is to avoid race condition that leads to broken output.
func writeOut(w io.Writer, url url.URL, message string) {
	outputMu.Lock()
	defer outputMu.Unlock()

	_, err := fmt.Fprintf(w, "%s %s\n", url.String(), message)
	mustNoError(err)
}

// mustNoError panics when there is an error.
func mustNoError(err error) {
	if err != nil {
		panic(err)
	}
}

// errToCode converts error to status code. If there is no error or the status is exitCodeError, the status code is left intact.
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
