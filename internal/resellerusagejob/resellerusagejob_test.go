package resellerusagejob

import (
	"context"
	"errors"
	"testing"

	"github.com/legendary1205/rapido-go/internal/db/generated"
	"github.com/legendary1205/rapido-go/internal/integrationsettings"
	"github.com/legendary1205/rapido-go/internal/resellerapi"
)

type fakeQueries struct {
	pending []generated.GetPendingResellerAPIUsageRow
	marked  []generated.MarkResellerAPIUsageReportedParams
	markErr error
}

func (f *fakeQueries) GetPendingResellerAPIUsage(context.Context) ([]generated.GetPendingResellerAPIUsageRow, error) {
	return f.pending, nil
}

func (f *fakeQueries) MarkResellerAPIUsageReported(ctx context.Context, arg generated.MarkResellerAPIUsageReportedParams) error {
	// A real database call fails on a canceled context, and so does this.
	if err := ctx.Err(); err != nil {
		return err
	}
	f.marked = append(f.marked, arg)
	return f.markErr
}

type fakeReporter struct {
	calls [][]resellerapi.Usage
	err   error
}

func (f *fakeReporter) ReportUsages(_ context.Context, _ resellerapi.Config, usages []resellerapi.Usage) error {
	f.calls = append(f.calls, usages)
	return f.err
}

func configured(context.Context) (integrationsettings.Values, error) {
	return integrationsettings.Values{ResellerApiSecret: "s", ResellerApiUrl: "http://bot"}, nil
}

func queued() []generated.GetPendingResellerAPIUsageRow {
	return []generated.GetPendingResellerAPIUsageRow{
		{AdminID: 3, Username: "reseller_a", Pending: 700},
		{AdminID: 9, Username: "reseller_b", Pending: 42},
	}
}

func TestTickSendsQueuedUsageAndMarksExactlyWhatWasSent(t *testing.T) {
	q := &fakeQueries{pending: queued()}
	r := &fakeReporter{}

	if err := Tick(context.Background(), q, r, configured); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if len(r.calls) != 1 || len(r.calls[0]) != 2 ||
		r.calls[0][0] != (resellerapi.Usage{Username: "reseller_a", Usage: 700}) ||
		r.calls[0][1] != (resellerapi.Usage{Username: "reseller_b", Usage: 42}) {
		t.Fatalf("reports = %+v", r.calls)
	}
	if len(q.marked) != 1 {
		t.Fatalf("marked %d times, want 1", len(q.marked))
	}
	m := q.marked[0]
	if len(m.AdminIds) != 2 || m.AdminIds[0] != 3 || m.AdminIds[1] != 9 || m.Sent[0] != 700 || m.Sent[1] != 42 {
		t.Errorf("marked = %+v", m)
	}
}

// The whole point of the queue: a report the bot did not accept stays
// queued, so the traffic is billed on a later tick instead of lost.
func TestTickLeavesUsageQueuedWhenTheReportFails(t *testing.T) {
	q := &fakeQueries{pending: queued()}
	r := &fakeReporter{err: errors.New("connection refused")}

	if err := Tick(context.Background(), q, r, configured); err == nil {
		t.Fatal("expected the delivery error to be returned")
	}
	if len(q.marked) != 0 {
		t.Errorf("usage marked as reported after a failed report: %+v", q.marked)
	}
}

func TestTickDoesNothingWhileTheIntegrationIsNotConfigured(t *testing.T) {
	q := &fakeQueries{pending: queued()}
	r := &fakeReporter{}
	secretOnly := func(context.Context) (integrationsettings.Values, error) {
		return integrationsettings.Values{ResellerApiSecret: "s"}, nil
	}

	for name, settings := range map[string]SettingsFunc{
		"nothing set": func(context.Context) (integrationsettings.Values, error) { return integrationsettings.Values{}, nil },
		"no url":      secretOnly,
	} {
		if err := Tick(context.Background(), q, r, settings); err != nil {
			t.Errorf("%s: Tick: %v", name, err)
		}
	}
	if len(r.calls) != 0 || len(q.marked) != 0 {
		t.Errorf("reports=%d marks=%d, want none", len(r.calls), len(q.marked))
	}
}

func TestTickSendsNothingWhenNothingIsQueued(t *testing.T) {
	q := &fakeQueries{}
	r := &fakeReporter{}
	if err := Tick(context.Background(), q, r, configured); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("sent an empty report: %+v", r.calls)
	}
}

func TestTickStillMarksUsageWhenShutdownStartsAfterTheBotAcceptedIt(t *testing.T) {
	q := &fakeQueries{pending: queued()}
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelOnReport{cancel: cancel}

	if err := Tick(ctx, q, r, configured); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(q.marked) != 1 {
		t.Errorf("marked %d times, want 1 - an accepted report left unmarked is charged twice", len(q.marked))
	}
}

type cancelOnReport struct{ cancel context.CancelFunc }

func (c *cancelOnReport) ReportUsages(context.Context, resellerapi.Config, []resellerapi.Usage) error {
	c.cancel()
	return nil
}
