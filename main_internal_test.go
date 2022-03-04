package main

import (
	"bytes"
	"context"
	"crypto/md5" // nolint: gosec
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRunMain_MaxWorkersError(t *testing.T) { // nolint: paralleltest
	runMainTest(t, []string{"-parallel=25"}, func(t *testing.T, code int, stdOut *bytes.Buffer, stdErr *bytes.Buffer) {
		t.Helper()

		err := "maximum workers is 24\n"

		assertEqual(t, exitCodeBadArgs, code)
		assertEqual(t, 0, stdOut.Len())
		assertEqual(t, err, stdErr.String())
	})
}

func TestRunMain_InvalidURL(t *testing.T) { // nolint: paralleltest
	runMainTest(t, []string{"ftp://google.com"}, func(t *testing.T, code int, stdOut *bytes.Buffer, stdErr *bytes.Buffer) {
		t.Helper()

		err := "parse \"ftp://google.com\": unsupported scheme \"ftp\"\n"

		assertEqual(t, exitCodeBadArgs, code)
		assertEqual(t, 0, stdOut.Len())
		assertEqual(t, err, stdErr.String())
	})
}

func TestRunMain_Success(t *testing.T) { // nolint: paralleltest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(req.URL.String())) // nolint: errcheck
	}))
	defer srv.Close()

	expectedUrls := []string{
		srv.URL + "/path2",
		srv.URL + "/path2",
	}

	runMainTest(t, expectedUrls, func(t *testing.T, code int, stdOut *bytes.Buffer, stdErr *bytes.Buffer) {
		t.Helper()

		assertEqual(t, exitCodeOK, code)
		assertEqual(t, 0, stdErr.Len())

		urls := make([]string, 0)

		for _, line := range strings.Split(stdOut.String(), "\n") {
			if line == "" {
				continue
			}

			parts := strings.SplitN(line, " ", 2)
			urls = append(urls, parts[0])

			assertEqual(t, 32, len(parts[1]))
		}

		sort.Strings(urls)

		assertEqual(t, expectedUrls, urls)
	})
}

func TestParseURLs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		scenario       string
		urls           []string
		expectedResult []url.URL
		expectedError  string
	}{
		{
			scenario:       "no urls",
			expectedResult: []url.URL{},
		},
		{
			scenario: "missing scheme is ok",
			urls:     []string{"google.com"},
			expectedResult: []url.URL{
				{
					Scheme: "https",
					Host:   "google.com",
				},
			},
		},
		{
			scenario: "url has path but missing scheme is ok",
			urls:     []string{"google.com/path"},
			expectedResult: []url.URL{
				{
					Scheme: "https",
					Host:   "google.com",
					Path:   "/path",
				},
			},
		},
		{
			scenario:      "unsupported scheme",
			urls:          []string{"ftp://google.com"},
			expectedError: `parse "ftp://google.com": unsupported scheme "ftp"`,
		},
		{
			scenario:      "missing hostname",
			urls:          []string{"/path"},
			expectedError: `parse "https:///path": missing hostname`,
		},
		{
			scenario:      "invalid url",
			urls:          []string{string([]byte{0x7f})},
			expectedError: `parse "https://\u007f": net/url: invalid control character in URL`,
		},
		{
			scenario: "multiple urls",
			urls:     []string{"google.com", "https://netflix.com", "http://facebook.com", "spotify.com/path"},
			expectedResult: []url.URL{
				{
					Scheme: "https",
					Host:   "google.com",
				},
				{
					Scheme: "https",
					Host:   "netflix.com",
				},
				{
					Scheme: "http",
					Host:   "facebook.com",
				},
				{
					Scheme: "https",
					Host:   "spotify.com",
					Path:   "/path",
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()

			result, err := parseURLs(tc.urls)

			assertEqual(t, tc.expectedResult, result)

			if tc.expectedError == "" {
				assertNoError(t, err)
			} else {
				assertEqualError(t, err, tc.expectedError)
			}
		})
	}
}

func TestHashStream_NoReader(t *testing.T) {
	t.Parallel()

	result, err := hashStream(nil)

	assertEqual(t, "d41d8cd98f00b204e9800998ecf8427e", result)
	assertNoError(t, err)
}

func TestHashStream_CouldNotRead(t *testing.T) {
	t.Parallel()

	result, err := hashStream(readerFn(func([]byte) (int, error) {
		return 0, errors.New("read error")
	}))

	assertEqual(t, "", result)
	assertEqualError(t, err, "could not read from source: read error")
}

func TestHashURL_TimedOut(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := hashURL(ctx, mustParseURL(srv.URL))

	assertEqual(t, "", result)
	assertErrorIs(t, err, context.DeadlineExceeded)
}

func TestHashURL_Canceled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		result, err := hashURL(ctx, mustParseURL(srv.URL))

		assertEqual(t, "", result)
		assertErrorIs(t, err, context.Canceled)
	}()

	time.Sleep(100 * time.Millisecond)

	cancel()
}

func TestHashURL_UnexpectedStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	result, err := hashURL(context.Background(), mustParseURL(srv.URL))

	assertEqual(t, "", result)
	assertEqualError(t, err, `request got unexpected code 204`)
}

func TestHashURL_EmptyBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	result, err := hashURL(context.Background(), mustParseURL(srv.URL))

	assertEqual(t, "d41d8cd98f00b204e9800998ecf8427e", result)
	assertNoError(t, err)
}

func TestHashURL_HasBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`hello world !`)) // nolint: errcheck
	}))
	defer srv.Close()

	result, err := hashURL(context.Background(), mustParseURL(srv.URL))

	assertEqual(t, "905138a85e85e74344e90d25dba7299e", result)
	assertNoError(t, err)
}

func TestHashURLs_NoURLs(t *testing.T) {
	t.Parallel()

	out := new(bytes.Buffer)
	outErr := new(bytes.Buffer)

	code := hashURLs(context.Background(), nil, 0, 0, out, outErr)

	assertEqual(t, exitCodeOK, code)
	assertEqual(t, 0, out.Len())
	assertEqual(t, 0, outErr.Len())
}

func TestHashURLs_ContextCanceled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/cancel") {
			time.Sleep(500 * time.Millisecond)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`hello world!`)) // nolint: errcheck
	}))
	defer srv.Close()

	out := new(bytes.Buffer)
	outErr := new(bytes.Buffer)
	urls := []url.URL{
		mustParseURL(srv.URL),
		mustParseURL(srv.URL + "/cancel"),
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	code := hashURLs(ctx, urls, 2, 400*time.Millisecond, out, outErr)

	expected := fmt.Sprintf("%s %s\n", srv.URL, md5Sum("hello world!"))

	assertEqual(t, exitCodeCanceled, code)
	assertEqual(t, expected, out.String())
	assertEqual(t, 0, outErr.Len())
}

func TestHashURLs_ContextDoneSignaled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(500 * time.Millisecond)

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`hello world!`)) // nolint: errcheck
	}))
	defer srv.Close()

	out := new(bytes.Buffer)
	outErr := new(bytes.Buffer)
	urls := []url.URL{
		mustParseURL(srv.URL),
		mustParseURL(srv.URL),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	code := hashURLs(ctx, urls, 2, time.Second, out, outErr)

	assertEqual(t, exitCodeCanceled, code)
	assertEqual(t, 0, out.Len())
	assertEqual(t, 0, outErr.Len())
}

func TestHashURLs_ContextTimedOut(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/timeout") {
			time.Sleep(500 * time.Millisecond)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`hello world!`)) // nolint: errcheck
	}))
	defer srv.Close()

	out := new(bytes.Buffer)
	outErr := new(bytes.Buffer)
	urls := []url.URL{
		mustParseURL(srv.URL),
		mustParseURL(srv.URL + "/timeout"),
	}

	code := hashURLs(context.Background(), urls, 2, 400*time.Millisecond, out, outErr)

	expected := fmt.Sprintf("%s %s\n", srv.URL, md5Sum("hello world!"))
	expectedErr := fmt.Sprintf("%s/timeout could not request: Get \"%s/timeout\": context deadline exceeded\n", srv.URL, srv.URL)

	assertEqual(t, exitCodeError, code)
	assertEqual(t, expected, out.String())
	assertEqual(t, expectedErr, outErr.String())
}

func TestHashURLs_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(req.URL.Path)) // nolint: errcheck
	}))
	defer srv.Close()

	out := new(bytes.Buffer)
	outErr := new(bytes.Buffer)
	urls := []url.URL{
		mustParseURL(srv.URL + "/path1"),
		mustParseURL(srv.URL + "/path2"),
		mustParseURL(srv.URL + "/path3"),
	}

	code := hashURLs(context.Background(), urls, 10, 400*time.Millisecond, out, outErr)

	assertEqual(t, exitCodeOK, code)
	assertEqual(t, 0, outErr.Len())

	actual := out.String()
	expected := []string{
		fmt.Sprintf("%s/path1 %s", srv.URL, md5Sum("/path1")),
		fmt.Sprintf("%s/path2 %s", srv.URL, md5Sum("/path2")),
		fmt.Sprintf("%s/path3 %s", srv.URL, md5Sum("/path3")),
	}

	for _, line := range expected {
		assertContains(t, actual, line)
	}
}

func TestMustNoError(t *testing.T) {
	t.Parallel()

	err := errors.New("panic")

	defer func() {
		r := recover()

		assertEqual(t, r, err)
	}()

	mustNoError(err)
}

func TestErrToCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		scenario string
		error    error
		code     int64
		expected int64
	}{
		{
			scenario: "code ok and no error",
			code:     exitCodeOK,
			expected: exitCodeOK,
		},
		{
			scenario: "code ok and context timed out",
			error:    context.DeadlineExceeded,
			code:     exitCodeOK,
			expected: exitCodeError,
		},
		{
			scenario: "code ok and context canceled",
			error:    context.Canceled,
			code:     exitCodeOK,
			expected: exitCodeCanceled,
		},
		{
			scenario: "code error and no error",
			code:     exitCodeError,
			expected: exitCodeError,
		},
		{
			scenario: "code error and context timed out",
			error:    context.DeadlineExceeded,
			code:     exitCodeError,
			expected: exitCodeError,
		},
		{
			scenario: "code error and context canceled",
			error:    context.Canceled,
			code:     exitCodeError,
			expected: exitCodeError,
		},
		{
			scenario: "code canceled and no error",
			code:     exitCodeCanceled,
			expected: exitCodeCanceled,
		},
		{
			scenario: "code canceled and context timed out",
			error:    context.DeadlineExceeded,
			code:     exitCodeCanceled,
			expected: exitCodeError,
		},
		{
			scenario: "code canceled and context canceled",
			error:    context.Canceled,
			code:     exitCodeCanceled,
			expected: exitCodeCanceled,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()

			errToCode(&tc.code, tc.error)

			assertEqual(t, tc.expected, tc.code)
		})
	}
}

type readerFn func(p []byte) (n int, err error)

func (f readerFn) Read(p []byte) (int, error) {
	return f(p)
}

func runMainTest(t *testing.T, args []string, expect func(t *testing.T, code int, stdOut *bytes.Buffer, stdErr *bytes.Buffer)) {
	t.Helper()

	curStdOut := os.Stdout
	curStdErr := os.Stderr
	curArgs := os.Args

	t.Cleanup(func() {
		stdOut = curStdOut
		stdErr = curStdErr
		os.Args = curArgs

		flag.Parse()
	})

	bufOut := new(bytes.Buffer)
	bufErr := new(bytes.Buffer)

	argNumWorkers = defaultNumWorkers
	stdOut = bufOut
	stdErr = bufErr

	os.Args = append([]string{t.Name()}, args...)

	expect(t, runMain(), bufOut, bufErr)
}

func mustParseURL(str string) url.URL {
	u, err := url.Parse(str)
	mustNoError(err)

	return *u
}

// md5 is needed for testing the result.
// nolint: gosec
func md5Sum(s string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(s)))
}

func assertEqual(tb testing.TB, expected interface{}, actual interface{}) {
	tb.Helper()

	if !reflect.DeepEqual(expected, actual) {
		tb.Errorf("expected %+v, got %+v", expected, actual)
	}
}

func assertContains(tb testing.TB, actual string, expected string) {
	tb.Helper()

	if !strings.Contains(actual, expected) {
		tb.Errorf("%q doesn not contain %q", actual, expected)
	}
}

func assertEqualError(tb testing.TB, err error, expected string) {
	tb.Helper()

	if err == nil {
		tb.Errorf("error is expected, got nothing")

		return
	}

	if err.Error() != expected {
		tb.Errorf("expected error %q, got %q", expected, err.Error())

		return
	}
}

func assertErrorIs(tb testing.TB, err error, expected error) {
	tb.Helper()

	if !errors.Is(err, expected) {
		if err != nil {
			tb.Errorf("expected error %q, got %q", expected.Error(), err.Error())
		}
	}
}

func assertNoError(tb testing.TB, err error) {
	tb.Helper()

	if err != nil {
		tb.Errorf("error is not expected, got %q", err.Error())
	}
}
