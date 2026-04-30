package confish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ActionStatus is the lifecycle state of an action.
type ActionStatus string

const (
	StatusPending      ActionStatus = "pending"
	StatusAcknowledged ActionStatus = "acknowledged"
	StatusCompleted    ActionStatus = "completed"
	StatusFailed       ActionStatus = "failed"
	StatusExpired      ActionStatus = "expired"
)

// ActionUpdate is a single timeline entry on an Action.
type ActionUpdate struct {
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
	Timestamp string         `json:"timestamp,omitempty"`
}

// Action mirrors the JSON shape returned by the actions API.
//
// Params and Result are kept as json.RawMessage so callers can decode them into
// their own types without forcing a generic on Action itself. Use Action.DecodeParams
// or Action.DecodeResult for ergonomic decoding.
type Action struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Params         json.RawMessage `json:"params"`
	Status         ActionStatus    `json:"status"`
	Updates        []ActionUpdate  `json:"updates"`
	Result         json.RawMessage `json:"result"`
	ExpiresAt      string          `json:"expires_at"`
	AcknowledgedAt string          `json:"acknowledged_at"`
	CompletedAt    string          `json:"completed_at"`
	CreatedAt      string          `json:"created_at"`
}

// DecodeParams decodes the action's params into out.
func (a Action) DecodeParams(out any) error {
	if len(a.Params) == 0 || string(a.Params) == "null" {
		return nil
	}
	return json.Unmarshal(a.Params, out)
}

// DecodeResult decodes the action's result into out.
func (a Action) DecodeResult(out any) error {
	if len(a.Result) == 0 || string(a.Result) == "null" {
		return nil
	}
	return json.Unmarshal(a.Result, out)
}

// Actions wraps the action management endpoints.
type Actions struct {
	client *Client
}

// List returns pending, non-expired actions ordered oldest first.
func (a *Actions) List(ctx context.Context) ([]Action, error) {
	var resp struct {
		Actions []Action `json:"actions"`
	}
	if err := a.client.do(ctx, http.MethodGet, "/c/"+a.client.envID+"/actions", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Actions, nil
}

// Ack acknowledges an action. Returns *ConflictError if the action is already
// acknowledged or no longer actionable.
func (a *Actions) Ack(ctx context.Context, actionID string) (Action, error) {
	return a.simpleAction(ctx, "/ack", actionID, nil)
}

// Update appends a timeline update to an action.
func (a *Actions) Update(ctx context.Context, actionID, message string, data map[string]any) (Action, error) {
	body := map[string]any{"message": message}
	if data != nil {
		body["data"] = data
	}
	return a.simpleAction(ctx, "/update", actionID, body)
}

// Complete marks an action as completed. result may be nil.
func (a *Actions) Complete(ctx context.Context, actionID string, result map[string]any) (Action, error) {
	return a.simpleAction(ctx, "/complete", actionID, resultBody(result))
}

// Fail marks an action as failed. result may be nil.
func (a *Actions) Fail(ctx context.Context, actionID string, result map[string]any) (Action, error) {
	return a.simpleAction(ctx, "/fail", actionID, resultBody(result))
}

func (a *Actions) simpleAction(ctx context.Context, suffix, actionID string, body any) (Action, error) {
	var out Action
	path := "/c/" + a.client.envID + "/actions/" + actionID + suffix
	err := a.client.do(ctx, http.MethodPost, path, body, &out)
	return out, err
}

func resultBody(result map[string]any) map[string]any {
	body := map[string]any{}
	if result != nil {
		body["result"] = result
	}
	return body
}

// ActionUpdater is provided to Handler so it can append timeline updates.
type ActionUpdater interface {
	Update(ctx context.Context, message string, data map[string]any) error
}

// ActionHandler processes a single action. Returning a non-nil result becomes the
// completion payload; returning a non-nil error fails the action with
// `{"error": err.Error()}` as the result. ErrSkipAction can be returned to leave
// the action acknowledged without completing or failing it (useful for graceful
// shutdown mid-handler).
type ActionHandler func(ctx context.Context, action Action, updater ActionUpdater) (result map[string]any, err error)

// ErrSkipAction tells the consumer not to complete or fail the action. The action
// will stay in the acknowledged state until it expires.
var ErrSkipAction = errors.New("confish: skip action")

// ConsumeOptions configure the action consumer loop.
type ConsumeOptions struct {
	// Handler runs for each pending action.
	Handler ActionHandler
	// PollInterval is the base delay between polls when no actions are pending.
	// After 3 consecutive empty polls the delay doubles each poll up to MaxPollInterval,
	// resetting to this base as soon as an action is processed. Default: 15 seconds.
	PollInterval time.Duration
	// MaxPollInterval caps the adaptive backoff delay. Default: 60 seconds.
	MaxPollInterval time.Duration
	// Concurrency is the maximum number of actions processed in parallel.
	// Default: 1 (sequential).
	Concurrency int
	// OnError is called when listing fails, when ack fails (other than 409),
	// or when a handler error occurs. Optional.
	OnError func(err error, action Action)
}

// Consume runs the polling loop until ctx is cancelled. Errors during listing
// are reported via OnError but do not stop the loop. Returns nil when ctx is done.
func (a *Actions) Consume(ctx context.Context, opts ConsumeOptions) error {
	if opts.Handler == nil {
		return fmt.Errorf("confish: ConsumeOptions.Handler is required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 15 * time.Second
	}
	if opts.MaxPollInterval <= 0 {
		opts.MaxPollInterval = 60 * time.Second
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}

	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	var emptyPolls int

	for {
		if ctx.Err() != nil {
			wg.Wait()
			return nil
		}

		actions, err := a.List(ctx)
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			if opts.OnError != nil {
				opts.OnError(err, Action{})
			}
			if !sleep(ctx, backoffDelay(emptyPolls, opts.PollInterval, opts.MaxPollInterval)) {
				wg.Wait()
				return nil
			}
			continue
		}

		pending := actions[:0]
		for _, act := range actions {
			if act.Status == StatusPending {
				pending = append(pending, act)
			}
		}

		if len(pending) == 0 {
			emptyPolls++
			if !sleep(ctx, backoffDelay(emptyPolls, opts.PollInterval, opts.MaxPollInterval)) {
				wg.Wait()
				return nil
			}
			continue
		}

		emptyPolls = 0

		for _, act := range pending {
			select {
			case <-ctx.Done():
				wg.Wait()
				return nil
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go func(act Action) {
				defer wg.Done()
				defer func() { <-sem }()
				a.processAction(ctx, act, opts)
			}(act)
		}
	}
}

func (a *Actions) processAction(ctx context.Context, action Action, opts ConsumeOptions) {
	if _, err := a.Ack(ctx, action.ID); err != nil {
		if IsConflict(err) || ctx.Err() != nil {
			return
		}
		if opts.OnError != nil {
			opts.OnError(err, action)
		}
		return
	}

	updater := &actionUpdater{actions: a, actionID: action.ID}
	result, err := opts.Handler(ctx, action, updater)

	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, ErrSkipAction) {
		return
	}
	if err != nil {
		if opts.OnError != nil {
			opts.OnError(err, action)
		}
		if _, failErr := a.Fail(ctx, action.ID, map[string]any{"error": err.Error()}); failErr != nil {
			if opts.OnError != nil && ctx.Err() == nil {
				opts.OnError(failErr, action)
			}
		}
		return
	}

	if _, completeErr := a.Complete(ctx, action.ID, result); completeErr != nil {
		if opts.OnError != nil && ctx.Err() == nil {
			opts.OnError(completeErr, action)
		}
	}
}

type actionUpdater struct {
	actions  *Actions
	actionID string
}

func (u *actionUpdater) Update(ctx context.Context, message string, data map[string]any) error {
	_, err := u.actions.Update(ctx, u.actionID, message, data)
	return err
}

// backoffDelay holds at base for the first 3 empty polls, then doubles each subsequent
// empty poll up to max. Reset to 0 (returning base) the moment work is processed.
func backoffDelay(emptyPolls int, base, max time.Duration) time.Duration {
	if emptyPolls <= 3 {
		return base
	}
	d := base << (emptyPolls - 3)
	if d <= 0 || d > max {
		return max
	}
	return d
}

// sleep returns false if ctx was cancelled during the wait.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
