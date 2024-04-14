package main

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/alecthomas/kong"
)

const defaultBenchArgsTmpl = `test -json {{ .Packages }} -run '^$'
{{- if .Bench }} -bench {{ .Bench }}{{end}}
{{- if .Count }} -count {{ .Count }}{{end}}
{{- if .Benchtime }} -benchtime {{ .Benchtime }}{{end}}
{{- if .CPU }} -cpu {{ .CPU }}{{ end }}
{{- if .Tags }} -tags "{{ .Tags }}"{{ end }}
{{- if .Benchmem }} -benchmem{{ end }}`

var benchVars = kong.Vars{
	"BenchCmdDefault":   `go`,
	"CountHelp":         `Run each benchmark n times. If --cpu is set, run n times for each GOMAXPROCS value.'`,
	"BenchHelp":         `Run only those benchmarks matching a regular expression. To run all benchmarks, use '--bench .'.`,
	"BenchmarkArgsHelp": `Override the default args to the go command. This may be a template. See https://github.com/willabides/benchdiff for details."`,
	"BenchtimeHelp":     `Run enough iterations of each benchmark to take t, specified as a time.Duration (for example, --benchtime 1h30s). The default is 1 second (1s). The special syntax Nx means to run the benchmark N times (for example, -benchtime 100x).`,
	"PackagesHelp":      `Run benchmarks in these packages.`,
	"BenchCmdHelp":      `The command to use for benchmarks.`,
	"BenchstatCmdHelp":  `The command to use for benchstat.`,
	"CacheDirHelp":      `Override the default directory where benchmark output is kept.`,
	"BaseRefHelp":       `The git ref to be used as a baseline.`,
	"HeadRefHelp":       `The git ref to be benchmarked. By default the worktree is used.`,
	"NoCacheHelp":       `Rerun benchmarks even if the output already exists.`,
	"GitCmdHelp":        `The executable to use for git commands.`,
	"VersionHelp":       `Output the benchdiff version and exit.`,
	"ClearCacheHelp":    `Remove benchdiff files from the cache dir.`,
	"CPUHelp":           `Specify a list of GOMAXPROCS values for which the benchmarks should be executed. The default is the current value of GOMAXPROCS.`,
	"BenchmemHelp":      `Memory allocation statistics for benchmarks.`,
	"TagsHelp":          `Set the -tags flag on the go test command`,
}

var groupHelp = kong.Vars{
	"gotestGroupHelp": "benchmark command line:",
	"cacheGroupHelp":  "benchmark result cache:",
}

var cli struct {
	Debug bool `kong:"help='write verbose output to stderr'"`

	BaseRef      string `kong:"default=HEAD,help=${BaseRefHelp},group='x'"`
	HeadRef      string `kong:"help=${BaseRefHelp},group='x'"`
	GitCmd       string `kong:"default=git,help=${GitCmdHelp},group='x'"`
	BenchstatCmd string `kong:"default=benchstat,help=${BenchstatCmdHelp},group='x'"`

	Bench         string  `kong:"default='.',help=${BenchHelp},group='gotest'"`
	BenchmarkArgs string  `kong:"placeholder='args',help=${BenchmarkArgsHelp},group='gotest'"`
	BenchmarkCmd  string  `kong:"default=${BenchCmdDefault},help=${BenchCmdHelp},group='gotest'"`
	Benchmem      bool    `kong:"help=${BenchmemHelp},group='gotest'"`
	Benchtime     string  `kong:"help=${BenchtimeHelp},group='gotest'"`
	Count         int     `kong:"default=10,help=${CountHelp},group='gotest'"`
	CPU           CPUFlag `kong:"help=${CPUHelp},group='gotest',placeholder='GOMAXPROCS,...'"`
	Packages      string  `kong:"default='./...',help=${PackagesHelp},group='gotest'"`
	Tags          string  `kong:"help=${TagsHelp},group='gotest'"`

	ClearCache ClearCacheFlag `kong:"help=${ClearCacheHelp},group='cache'"`
	NoCache    bool           `kong:"help=${NoCacheHelp},group='cache'"`
}

// ClearCacheFlag flag for clearing cache
type ClearCacheFlag bool

// AfterApply clears cache
func (v ClearCacheFlag) AfterApply(app *kong.Kong) error {
	cacheDir, err := getCacheDir()
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(cacheDir, "benchdiff-*.out"))
	if err != nil {
		return fmt.Errorf("error finding files in %s: %v", cacheDir, err)
	}
	for _, file := range files {
		err = os.Remove(file)
		if err != nil {
			return fmt.Errorf("error removing %s: %v", file, err)
		}
	}
	app.Exit(0)
	return nil
}

func getCacheDir() (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("error finding user cache dir: %v", err)
	}
	return filepath.Join(userCacheDir, "benchdiff"), nil
}

// CPUFlag is the flag for --cpu
type CPUFlag []int

func (c CPUFlag) String() string {
	s := make([]string, len(c))
	for i, cc := range c {
		s[i] = strconv.Itoa(cc)
	}
	return strings.Join(s, ",")
}

func getBenchArgs() (string, error) {
	argsTmpl := cli.BenchmarkArgs
	if argsTmpl == "" {
		argsTmpl = defaultBenchArgsTmpl
	}
	tmpl, err := template.New("").Parse(argsTmpl)
	if err != nil {
		return "", err
	}
	var benchArgs bytes.Buffer
	err = tmpl.Execute(&benchArgs, cli)
	if err != nil {
		return "", err
	}
	args := benchArgs.String()
	return args, nil
}

const description = `
benchdiff runs go benchmarks on your current git worktree and a base ref then
uses benchstat to show the delta.

More documentation at https://github.com/willabides/benchdiff.
`

func main() {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintf(os.Stdout, "error finding user cache dir: %v\n", err)
		os.Exit(1)
	}
	benchVars["CacheDirDefault"] = filepath.Join(userCacheDir, "benchdiff")

	kctx := kong.Parse(&cli, benchVars, groupHelp,
		kong.Description(strings.TrimSpace(description)),
		kong.ExplicitGroups([]kong.Group{
			{Key: "cache", Title: "benchmark result cache"},
			{Key: "gotest", Title: "benchmark command line"},
			{Key: "x"},
		}),
	)

	benchArgs, err := getBenchArgs()
	kctx.FatalIfErrorf(err)

	cacheDir, err := getCacheDir()
	kctx.FatalIfErrorf(err)

	bd := &Benchdiff{
		GoCmd:      cli.BenchmarkCmd,
		BenchArgs:  benchArgs,
		ResultsDir: cacheDir,
		BaseRef:    cli.BaseRef,
		HeadRef:    cli.HeadRef,
		Force:      cli.NoCache,
		GitCmd:     cli.GitCmd,
	}
	if cli.Debug {
		bd.Debug = log.New(os.Stderr, "", 0)
	}
	result, err := bd.Run()
	kctx.FatalIfErrorf(err)

	cmd := exec.Command(cli.BenchstatCmd, result.BaseRef+"="+result.BaseOutputFile,
		result.HeadRef+"="+result.HeadOutputFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if cli.Debug {
		bd.Debug.Printf("+ %s", cmd)
	}
	err = cmd.Run()
	kctx.FatalIfErrorf(err)
}

type Benchdiff struct {
	GoCmd      string
	BenchArgs  string
	ResultsDir string
	BaseRef    string
	HeadRef    string
	GitCmd     string
	Force      bool
	Debug      *log.Logger
}

type RunResult struct {
	HeadOutputFile string
	BaseOutputFile string
	BenchmarkCmd   string
	HeadRef        string
	BaseRef        string
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	if err != nil {
		return !os.IsNotExist(err)
	}
	return true
}

func (c *Benchdiff) debug() *log.Logger {
	if c.Debug == nil {
		return log.New(io.Discard, "", 0)
	}
	return c.Debug
}

// runCmd runs cmd sending its stdout and stderr to debug.Write()
func runCmd(cmd *exec.Cmd, debug *log.Logger) error {
	if debug == nil {
		debug = log.New(io.Discard, "", 0)
	}
	var bufStderr bytes.Buffer
	stderr := io.MultiWriter(&bufStderr, debug.Writer())
	if cmd.Stderr != nil {
		stderr = io.MultiWriter(cmd.Stderr, stderr)
	}
	cmd.Stderr = stderr
	stdout := debug.Writer()
	if cmd.Stdout != nil {
		stdout = io.MultiWriter(cmd.Stdout, stdout)
	}
	cmd.Stdout = stdout
	debug.Printf("+ %s", cmd)
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		err = fmt.Errorf(`error running command: %s
exit code: %d
stderr: %s`, cmd.String(), exitErr.ExitCode(), bufStderr.String())
	}
	return err
}

func (c *Benchdiff) runBenchmark(ref, filename string, force bool) error {
	cmd := exec.Command(c.GoCmd, strings.Fields(c.BenchArgs)...)

	stdlib := false
	if rootPath, err := c.runGitCmd("rev-parse", "--show-toplevel"); err == nil {
		// lib/time/zoneinfo.zip is a specific enough path, and it's here to
		// stay because it's one of the few paths hardcoded into Go binaries.
		zoneinfoPath := filepath.Join(string(rootPath), "lib", "time", "zoneinfo.zip")
		if _, err := os.Stat(zoneinfoPath); err == nil {
			stdlib = true
			c.debug().Println("standard library detected")
			cmd.Path = filepath.Join(string(rootPath), "bin", "go")
		}
	}

	fileBuffer := &bytes.Buffer{}
	cmd.Stdout = &TestJSONWriter{f: func(e *TestEvent) {
		if e.Action == "output" {
			io.WriteString(fileBuffer, e.Output)
		}
	}}

	if filename != "" {
		c.debug().Printf("output file: %s", filename)
		if ref != "" && !force {
			if fileExists(filename) {
				c.debug().Printf("+ skipping benchmark for ref %q because output file exists", ref)
				return nil
			}
		}
	}

	if !stdlib {
		goVersion, err := c.runGoCmd("env", "GOVERSION")
		if err != nil {
			return err
		}
		fmt.Fprintf(fileBuffer, "go: %s\n", goVersion)
	}

	var runErr error
	if ref == "" {
		runErr = runCmd(cmd, c.debug())
	} else {
		err := c.runAtGitRef(c.BaseRef, func(workPath string) {
			if stdlib {
				makeCmd := exec.Command(filepath.Join(workPath, "src", "make.bash"))
				makeCmd.Dir = filepath.Join(workPath, "src")
				makeCmd.Env = append(os.Environ(), "GOOS=", "GOARCH=")
				runErr = runCmd(makeCmd, c.debug())
				if runErr != nil {
					return
				}
				cmd.Path = filepath.Join(workPath, "bin", "go")
			}
			cmd.Dir = workPath // TODO: add relative path of working directory
			runErr = runCmd(cmd, c.debug())
		})
		if err != nil {
			return err
		}
	}
	if runErr != nil {
		return runErr
	}
	if filename == "" {
		return nil
	}
	return os.WriteFile(filename, fileBuffer.Bytes(), 0o666)
}

func (c *Benchdiff) countBenchmarks() (int, error) {
	var count int

	benchArgs := c.BenchArgs + " -benchtime 1ns"
	cmd := exec.Command(c.GoCmd, strings.Fields(benchArgs)...)
	cmd.Stdout = &TestJSONWriter{f: func(e *TestEvent) {
		// Unfortunately, the go test -json output makes it hard to track timing
		// output lines without heuristics. See https://go.dev/issue/66825.
		if e.Action == "output" && strings.Contains(e.Output, "\t") &&
			strings.HasPrefix(e.Output, "Benchmark") {
			count++
		}
	}}

	err := runCmd(cmd, c.debug())
	return count, err
}

func (c *Benchdiff) Run() (result *RunResult, err error) {
	if err := os.MkdirAll(c.ResultsDir, 0o700); err != nil {
		return nil, err
	}

	headFlag := "--dirty"
	if c.HeadRef != "" {
		headFlag = c.HeadRef
	}
	headRef, err := c.runGitCmd("describe", "--tags", "--always", headFlag)
	if err != nil {
		return nil, err
	}
	headFilename, err := c.cacheFilename(string(headRef))
	if err != nil {
		return nil, err
	}

	baseRef, err := c.runGitCmd("describe", "--tags", "--always", c.BaseRef)
	if err != nil {
		return nil, err
	}
	baseFilename, err := c.cacheFilename(string(baseRef))
	if err != nil {
		return nil, err
	}

	count, err := c.countBenchmarks()
	if err != nil {
		return nil, err
	}
	c.debug().Printf("counted %d benchmarks", count)

	result = &RunResult{
		BenchmarkCmd:   fmt.Sprintf("%s %s", c.GoCmd, c.BenchArgs),
		HeadRef:        strings.TrimSpace(string(headRef)),
		BaseRef:        strings.TrimSpace(string(baseRef)),
		BaseOutputFile: baseFilename,
		HeadOutputFile: headFilename,
	}

	err = c.runBenchmark(c.BaseRef, baseFilename, c.Force)
	if err != nil {
		return nil, err
	}

	err = c.runBenchmark(c.HeadRef, headFilename, c.Force)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (c *Benchdiff) cacheFilename(ref string) (string, error) {
	env, err := c.runGoCmd("env", "GOARCH", "GOEXPERIMENT", "GOOS", "GOVERSION", "CC", "CXX", "CGO_ENABLED", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS")
	if err != nil {
		return "", err
	}
	rootPath, err := c.runGitCmd("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}

	h := sha512.New()
	fmt.Fprintf(h, "%s\n", c.GoCmd)
	fmt.Fprintf(h, "%s\n", c.BenchArgs)
	fmt.Fprintf(h, "%s\n", env)
	fmt.Fprintf(h, "%s\n", ref)
	fmt.Fprintf(h, "%s\n", rootPath)
	cacheKey := base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])

	return filepath.Join(c.ResultsDir, fmt.Sprintf("benchdiff-%s.out", cacheKey)), nil
}

func (c *Benchdiff) runGoCmd(args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	cmd := exec.Command(c.GoCmd, args...)
	cmd.Stdout = &stdout
	err := runCmd(cmd, c.debug())
	return bytes.TrimSpace(stdout.Bytes()), err
}

func (c *Benchdiff) runGitCmd(args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	cmd := exec.Command(c.GitCmd, args...)
	cmd.Stdout = &stdout
	err := runCmd(cmd, c.debug())
	return bytes.TrimSpace(stdout.Bytes()), err
}

func (c *Benchdiff) runAtGitRef(ref string, fn func(path string)) error {
	worktree, err := os.MkdirTemp("", "benchdiff")
	if err != nil {
		return err
	}
	defer func() {
		rErr := os.RemoveAll(worktree)
		if rErr != nil {
			fmt.Printf("Could not delete temp directory: %s\n", worktree)
		}
	}()

	_, err = c.runGitCmd("worktree", "add", "--quiet", "--detach", worktree, ref)
	if err != nil {
		return err
	}

	defer func() {
		_, cerr := c.runGitCmd("worktree", "remove", worktree)
		if cerr != nil {
			if exitErr, ok := cerr.(*exec.ExitError); ok {
				fmt.Println(string(exitErr.Stderr))
			}
			fmt.Println(cerr)
		}
	}()
	fn(worktree)
	return nil
}

type TestEvent struct {
	Time    time.Time // encodes as an RFC3339-format string
	Action  string
	Package string
	Test    string
	Elapsed float64 // seconds
	Output  string
}

type TestJSONWriter struct {
	f   func(e *TestEvent)
	buf []byte
}

func (w *TestJSONWriter) Write(p []byte) (n int, err error) {
	w.buf = append(w.buf, p...)

	var offset int64
	defer func() { w.buf = w.buf[offset:] }()
	d := json.NewDecoder(bytes.NewReader(w.buf))
	for {
		e := &TestEvent{}
		err := d.Decode(e)
		if err == io.EOF {
			return len(p), nil
		}
		if err != nil {
			return 0, err
		}
		offset = d.InputOffset()
		w.f(e)
	}
}
