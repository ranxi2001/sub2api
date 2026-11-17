//go:build unit

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const faultBucket = "17:openai:single"

type faultNodeReply struct {
	URL   string
	Error string
}

// This helper re-executes only in the isolated test's child processes. It never
// loads deployment config or credentials. The publisher uses the real fenced
// publication adapter; the reader uses the real service and HTTP lifecycle.
func TestGatewaySnapshotNodeProcess(t *testing.T) {
	role := os.Getenv("SUB2API_TEST_SNAPSHOT_ROLE")
	if role == "" {
		return
	}
	require.Contains(t, []string{"publisher", "reader"}, role)
	out := os.NewFile(3, "node-replies")
	require.NotNil(t, out)
	t.Cleanup(func() { _ = out.Close() })
	reply := json.NewEncoder(out)
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: os.Getenv("SUB2API_TEST_SNAPSHOT_SOCKET"), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewSchedulerCache(client)
	bucket := service.SchedulerBucket{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	ctx := context.Background()
	var token service.SchedulerBucketWriteToken
	if role == "publisher" {
		var err error
		token, err = cache.CaptureBucketWriteToken(ctx, bucket)
		require.NoError(t, err)
	}
	var lifecycle *Lifecycle
	initial := faultNodeReply{}
	if role == "reader" {
		db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		cfg := &config.Config{Runtime: config.RuntimeConfig{Role: config.RuntimeRoleGateway, SchedulerReadiness: config.SchedulerReadinessConfig{Enabled: true, Buckets: []string{faultBucket}, MaxAgeSeconds: 2}}}
		scheduler := service.NewSchedulerSnapshotService(cache, nil, nil, nil, cfg)
		t.Cleanup(scheduler.Stop)
		lifecycle = ProvideLifecycle(cfg, db, client, scheduler)
		business := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			accounts, _, err := scheduler.ListSchedulableAccounts(r.Context(), &bucket.GroupID, bucket.Platform, false)
			if err != nil || len(accounts) != 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			account, err := scheduler.GetAccount(r.Context(), accounts[0].ID)
			if err != nil || account == nil || account.ID != 7 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("X-Test-Node", "reader")
			_, _ = io.WriteString(w, "fixture-ok")
		})
		handler := lifecycle.Wrap(business)
		// DB is only a Ping mock. The account repositories are deliberately nil:
		// a reader fallback cannot accidentally contact any SQL service.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/readyz" && !lifecycle.isDraining() {
				mock.ExpectPing()
			}
			handler.ServeHTTP(w, r)
		}))
		t.Cleanup(server.Close)
		t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
		initial.URL = server.URL
	}
	require.NoError(t, reply.Encode(initial))
	commands := json.NewDecoder(os.Stdin)
	for {
		var op string
		if err := commands.Decode(&op); err == io.EOF {
			return
		} else {
			require.NoError(t, err)
		}
		var err error
		switch {
		case role == "publisher" && op == "publish":
			err = cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true}})
		case role == "publisher" && op == "retire":
			err = cache.RetireBucket(ctx, bucket)
		case role == "publisher" && op == "reopen":
			token, err = cache.ReopenBucket(ctx, bucket)
		case role == "reader" && op == "drain":
			lifecycle.BeginDrain()
		case op == "exit":
			require.NoError(t, reply.Encode(faultNodeReply{}))
			return
		default:
			t.Fatal("unexpected test command")
		}
		result := faultNodeReply{}
		if err != nil {
			result.Error = "publication rejected"
		}
		require.NoError(t, reply.Encode(result))
	}
}

type faultNode struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *os.File
	encoder *json.Encoder
	decoder *json.Decoder
	url     string
	stopped bool
}

func startFaultNode(t *testing.T, role, socket string) *faultNode {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	readReply, writeReply, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = readReply.Close() })
	//nolint:gosec // G204: re-exec only this test executable, with fixed flags and fixture-only environment.
	cmd := exec.Command(executable, "-test.run=^TestGatewaySnapshotNodeProcess$", "-test.timeout=30s")
	cmd.Env = []string{"SUB2API_TEST_SNAPSHOT_ROLE=" + role, "SUB2API_TEST_SNAPSHOT_SOCKET=" + socket, "GOMAXPROCS=1", "GOMEMLIMIT=700MiB", "GOGC=20"}
	cmd.ExtraFiles = []*os.File{writeReply}
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	log, err := os.CreateTemp(t.TempDir(), role+"-*.log")
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })
	cmd.Stdout, cmd.Stderr = log, log
	require.NoError(t, cmd.Start())
	require.NoError(t, writeReply.Close())
	node := &faultNode{cmd: cmd, in: input, out: readReply, encoder: json.NewEncoder(input), decoder: json.NewDecoder(readReply)}
	t.Cleanup(func() {
		if !node.stopped {
			_ = node.cmd.Process.Kill()
			_ = node.cmd.Wait()
		}
		_ = input.Close()
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			t.Logf("isolated %s child log: %s", role, data)
		}
	})
	node.url = node.read(t).URL
	return node
}

func (n *faultNode) read(t *testing.T) faultNodeReply {
	t.Helper()
	require.NoError(t, n.out.SetReadDeadline(time.Now().Add(10*time.Second)))
	var result faultNodeReply
	require.NoError(t, n.decoder.Decode(&result), "isolated child must respond within its budget")
	return result
}

func (n *faultNode) command(t *testing.T, op string) faultNodeReply {
	t.Helper()
	require.NoError(t, n.encoder.Encode(op))
	return n.read(t)
}

func (n *faultNode) crash(t *testing.T) {
	t.Helper()
	require.NoError(t, n.cmd.Process.Kill())
	require.Error(t, n.cmd.Wait(), "a crash must actually stop the publisher process")
	n.stopped = true
}

func (n *faultNode) exit(t *testing.T) {
	t.Helper()
	require.Empty(t, n.command(t, "exit").Error)
	require.NoError(t, n.cmd.Wait())
	n.stopped = true
}

func TestGatewaySnapshotMultiProcessFaultRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("isolated subprocess fixture uses Unix sockets and inherited file descriptors")
	}
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required; backend CI explicitly installs it")
	}
	// Keep the socket name short even when CI's TMPDIR is a long data-disk path.
	temp, err := os.MkdirTemp("", "s2-fault-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(temp) })
	socket := filepath.Join(temp, "r.sock")
	//nolint:gosec // G204: fixed tool name resolved from test runner PATH; all arguments are isolated fixtures.
	redisProcess := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	redisProcess.Stdout, redisProcess.Stderr = io.Discard, io.Discard
	require.NoError(t, redisProcess.Start())
	t.Cleanup(func() { _ = redisProcess.Process.Kill(); _ = redisProcess.Wait() })
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	require.Eventually(t, func() bool { return client.Ping(ctx).Err() == nil }, 3*time.Second, 10*time.Millisecond)
	publisher := startFaultNode(t, "publisher", socket)
	reader := startFaultNode(t, "reader", socket)
	require.NotEqual(t, publisher.cmd.Process.Pid, reader.cmd.Process.Pid)
	require.NotEqual(t, os.Getpid(), reader.cmd.Process.Pid)
	target, err := url.Parse(reader.url)
	require.NoError(t, err)
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: transport, Timeout: time.Second}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	var admitted atomic.Bool
	var forwarded atomic.Int64
	// This is a test-only single-target sampler/router, not a shipping LB mode,
	// DNS controller, health monitor policy or claim about availability elsewhere.
	ingress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !admitted.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		forwarded.Add(1)
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(ingress.Close)
	status := func(endpoint string) (int, string) {
		t.Helper()
		response, err := httpClient.Get(endpoint)
		require.NoError(t, err)
		defer func() { _ = response.Body.Close() }()
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		return response.StatusCode, response.Header.Get("X-Test-Node")
	}
	sample := func() int {
		code, _ := status(reader.url + "/readyz")
		admitted.Store(code == http.StatusOK)
		return code
	}
	assertRoute := func(stage string, ready bool) {
		t.Helper()
		wantReadiness := http.StatusServiceUnavailable
		if ready {
			wantReadiness = http.StatusOK
		}
		readiness := sample()
		require.Equal(t, wantReadiness, readiness, stage)
		before := forwarded.Load()
		code, node := status(ingress.URL + "/work")
		if ready {
			require.Equal(t, http.StatusOK, code, stage)
			require.Equal(t, "reader", node, stage)
			require.Equal(t, before+1, forwarded.Load(), stage)
		} else {
			require.Equal(t, http.StatusServiceUnavailable, code, stage)
			require.Empty(t, node, stage)
			require.Equal(t, before, forwarded.Load(), "withdrawn target must receive no fixture business request")
		}
		code, _ = status(reader.url + "/health")
		require.Equal(t, http.StatusOK, code, "withdrawal does not stop reader liveness")
		t.Logf("stage=%s readiness_http=%d routed=%t", stage, readiness, ready)
	}
	publish := func() { t.Helper(); require.Empty(t, publisher.command(t, "publish").Error) }
	assertRoute("cold-start", false)
	publish()
	assertRoute("first-publish", true)
	publisher.crash(t)
	require.Eventually(t, func() bool { return sample() == http.StatusServiceUnavailable }, 6*time.Second, 50*time.Millisecond)
	assertRoute("publisher-stopped-and-stale", false)
	publisher = startFaultNode(t, "publisher", socket)
	publish()
	assertRoute("publisher-restarted", true)
	for _, kind := range []string{"members", "metadata", "full-account"} {
		version, err := client.Get(ctx, "sched:active:"+faultBucket).Result()
		require.NoError(t, err)
		key := "sched:" + faultBucket + ":v" + version
		if kind == "metadata" {
			key = "sched:meta:7"
		} else if kind == "full-account" {
			key = "sched:acc:7"
		}
		removed, err := client.Del(ctx, key).Result()
		require.NoError(t, err)
		require.EqualValues(t, 1, removed, "fault injection must delete the actual published cache key")
		assertRoute("lost-"+kind, false)
		unchanged, err := client.Get(ctx, "sched:active:"+faultBucket).Result()
		require.NoError(t, err)
		require.Equal(t, version, unchanged, "reader probe must not republish missing data")
		publish()
		assertRoute("repaired-"+kind, true)
	}
	require.Empty(t, publisher.command(t, "retire").Error)
	assertRoute("retired", false)
	require.Equal(t, "publication rejected", publisher.command(t, "publish").Error)
	assertRoute("retired-writer-fenced", false)
	require.Empty(t, publisher.command(t, "reopen").Error)
	assertRoute("reopened-before-publish", false)
	publish()
	assertRoute("reopened-and-published", true)
	require.Empty(t, reader.command(t, "drain").Error)
	assertRoute("reader-draining", false)
	reader.exit(t)
	publisher.exit(t)
}
