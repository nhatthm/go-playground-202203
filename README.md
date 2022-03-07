# Playground 202203

[![Build Status](https://github.com/nhatthm/go-playground-202203/actions/workflows/test.yaml/badge.svg)](https://github.com/nhatthm/go-playground-202203/actions/workflows/test.yaml)
[![codecov](https://codecov.io/gh/nhatthm/go-playground-202203/branch/master/graph/badge.svg?token=eTdAgDE2vR)](https://codecov.io/gh/nhatthm/go-playground-202203)
[![Go Report Card](https://goreportcard.com/badge/github.com/nhatthm/go-playground-202203)](https://goreportcard.com/report/github.com/nhatthm/go-playground-202203)

A tool to print out md5 checksum of a list of urls.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Usage](#usage)
- [Test](#Test)
- [Examples](#examples)

## Prerequisites

- `Go >= 1.17`

[<sub><sup>[table of contents]</sup></sub>](#table-of-contents)

## Usage

Build the project with `make build`, then run `./build/playground-220203 [OPTS] URL1 URL2 ...URLN`

Usage:

```bash
$ ./build/playground-220203 [-parallel NUM_WORKERS] URL1 URL2 ...URLN
```

- The `-parallel` is optional. If omitted, the default value is `10`. Only an integer between `1` and `24` is accepted.
- The URLs could be provided without a `scheme`, for example `google.com`. In case of omitting, `https` is automatically prepended to the url.

For example:

```bash
$ ./build/playground-220203 -parallel 5 google.com https://facebook.com
https://google.com ced3bb8a775295ff8e682049cc0d266c
https://facebook.com 69425f8815a6dde799621b8dab4aba2c
```

[<sub><sup>[table of contents]</sup></sub>](#table-of-contents)

## Test

Run `make test`

[<sub><sup>[table of contents]</sup></sub>](#table-of-contents)

## Examples

Run the built-in example with `make example` or `PARALLEL=24 make example`

[<sub><sup>[table of contents]</sup></sub>](#table-of-contents)
