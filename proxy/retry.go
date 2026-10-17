package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	bastion "github.com/xraph/bastion"
)

// maxReplayBody is the largest request body the proxy holds in memory so
// it can send it more than once. A bigger body goes upstream once.
const maxReplayBody = 1 << 20 // 1 MiB

// SetRetryPolicy sets the policy that decides whether a failed upstream
// attempt is retried and how long to wait first. Without one the proxy
// sends every request once.
func (pe *Engine) SetRetryPolicy(p bastion.RetryPolicy) { pe.retry = p }

// effectiveRetry returns the retry block that applies to route: the
// route's own when it has one, the gateway-wide one otherwise. It is the
// same rule resilience.RetryExecutor uses.
func effectiveRetry(route *bastion.Route, global bastion.RetryConfig) bastion.RetryConfig {
	if route != nil && route.Retry != nil {
		return *route.Retry
	}

	return global
}

// mayRetry reports whether a request with this method could be retried at
// all under cfg, before anything is sent.
func mayRetry(cfg bastion.RetryConfig, method string) bool {
	if !cfg.Enabled || cfg.MaxAttempts < 2 {
		return false
	}

	for _, m := range cfg.RetryableMethods {
		if strings.EqualFold(m, method) {
			return true
		}
	}

	return false
}

// bufferBody reads r's body into memory so it can be sent again, and puts
// it back on r. It returns the body (nil when r has none) and whether the
// body can be replayed. A body over limit is left to stream: r gets back
// what was read followed by the rest, and replayable is false.
func bufferBody(r *http.Request, limit int64) (body []byte, replayable bool, err error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, true, nil
	}

	if r.ContentLength > limit {
		return nil, false, nil
	}

	orig := r.Body

	buf, err := io.ReadAll(io.LimitReader(orig, limit+1))
	if err != nil {
		return nil, false, err
	}

	if int64(len(buf)) > limit {
		r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(buf), orig), Closer: orig}

		return nil, false, nil
	}

	r.Body = readCloser{Reader: bytes.NewReader(buf), Closer: orig}
	r.ContentLength = int64(len(buf))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf)), nil }

	return buf, true, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// accountedError marks an upstream error whose failure the retry loop has
// already recorded on the breaker and health monitor, so the proxy's
// error handler doesn't count it twice.
type accountedError struct{ err error }

func (e accountedError) Error() string { return e.err.Error() }
func (e accountedError) Unwrap() error { return e.err }

// retryTransport sends a request to one target and sends it again while
// the retry policy says the outcome is worth retrying. The proxy's
// response and error handlers only see the last attempt, so the attempts
// before it are accounted for here.
//
// It retries the same target, and only while that target's breaker is
// closed: a half-open breaker is letting a probe through, and a probe
// isn't retried.
type retryTransport struct {
	base   http.RoundTripper
	policy bastion.RetryPolicy
	pe     *Engine
	route  *bastion.Route
	target *bastion.Target
	cb     bastion.Breaker
	body   []byte // the buffered request body, nil when there is none

	// budget caps retries to budgetPercent of the target's recent
	// requests. Nil leaves retries unlimited.
	budget        *retryBudget
	budgetPercent float64
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		resp, err := t.base.RoundTrip(req)

		// A transport error is what the client would see as a 502.
		status := http.StatusBadGateway
		if err == nil {
			status = resp.StatusCode
		}

		if req.Context().Err() != nil ||
			t.cb.State() != bastion.CircuitClosed ||
			!t.policy.ShouldRetry(req.Method, status, attempt, t.route.Retry) {
			return resp, err
		}

		// Over budget: the client gets this attempt, as if it were the last.
		if t.budget != nil && !t.budget.tryRetry(t.pe.now(), t.budgetPercent) {
			t.pe.stats.RecordRetryBudgetExhausted()
			t.pe.metrics.RetryBudgetExhausted()

			return resp, err
		}

		if err != nil {
			t.cb.RecordFailure()

			if t.cb.State() != bastion.CircuitClosed {
				// That failure opened the breaker. Stop here.
				t.pe.hm.RecordPassiveFailure(t.target.ID)

				return nil, accountedError{err: err}
			}
		} else {
			_, _ = io.CopyN(io.Discard, resp.Body, 4<<10)
			_ = resp.Body.Close()
		}

		t.pe.hm.RecordPassiveFailure(t.target.ID)
		t.pe.stats.RecordRetryAttempt()

		if err := sleepCtx(req.Context(), t.policy.Delay(attempt-1, t.route.Retry)); err != nil {
			return nil, accountedError{err: err}
		}

		t.pe.metrics.RetrySent()

		req = req.Clone(req.Context())
		if len(t.body) > 0 {
			req.Body = io.NopCloser(bytes.NewReader(t.body))
			req.ContentLength = int64(len(t.body))
		}
	}
}

// sleepCtx waits for d, or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isAccounted reports whether err was already recorded by the retry loop.
func isAccounted(err error) bool {
	var ae accountedError

	return errors.As(err, &ae)
}
