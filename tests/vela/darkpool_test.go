// The dark pool on Vela's own runtime: build/app.wasm loaded with Vela's pkg/wasm (the code the
// Executor runs), not with Synsema's interpreter. `synsema test` proves the logic natively; this
// proves the module behaves the same inside the guest: labels, the output policy, the state's size.
//
//	sh scripts/build.sh && cd tests/vela && go test -v ./...
//	APP_WASM=/path/to/app.wasm go test -v ./...
//
// Linux only (Vela's runtime uses named pipes for the guest's logs), Go 1.24+ and a C compiler.
// The Executor adds two checks around the runtime that this file applies by hand: the state size
// limit (EXECUTOR_MAX_APP_STATE_SIZE, 10 MiB) and the fee floor; nothing else sits in between.
package vela

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HorizenOfficial/vela/pkg/common"
	"github.com/HorizenOfficial/vela/pkg/logger"
	"github.com/HorizenOfficial/vela/pkg/wasm"
	ethCommon "github.com/ethereum/go-ethereum/common"
)

const (
	maxStateSize = 10 * 1024 * 1024 // EXECUTOR_MAX_APP_STATE_SIZE, Vela 0.2.1
	eth          = "0x0000000000000000000000000000000000000000"
	tst          = "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	seller       = "0x1111111111111111111111111111111111111111"
	ana          = "0x2222222222222222222222222222222222222222"
	bob          = "0x3333333333333333333333333333333333333333"
	carl         = "0x4444444444444444444444444444444444444444"
	id1          = "0x00000000000000000000000000000001"
)

var (
	module []byte
	rt     *wasm.WasmtimeRuntime
	nextID uint64 = 100
)

func TestMain(m *testing.M) {
	path := os.Getenv("APP_WASM")
	if path == "" {
		path = "../../build/app.wasm"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no module at %s (sh scripts/build.sh first): %v\n", path, err)
		os.Exit(2)
	}
	module = b
	cfg := logger.DefaultLogConfig("printf")
	cfg.ConsoleLevel = os.Getenv("VELA_LOG_LEVEL")
	if cfg.ConsoleLevel == "" {
		cfg.ConsoleLevel = "warn"
	}
	rt = wasm.NewWasmtimeRuntime(logger.NewLogger(&cfg), 4)
	code := m.Run()
	rt.Close()
	os.Exit(code)
}

// ---- one deployed app, driven the way the Executor drives it ----

type event struct {
	User string
	Raw  []byte         // the plaintext the Executor encrypts: its size is public (+28 bytes)
	Data map[string]any // decoded
}

type result struct {
	Events      []event
	AppEvents   int
	Withdrawals []common.Withdrawal
	Report      []byte
	Fuel        *big.Int
	Failure     string // the public error message, "" on success
	State       []byte
	Took        time.Duration
}

type app struct {
	t     *testing.T
	id    common.ApplicationIdType
	state []byte
}

func deploy(t *testing.T, params string) *app {
	t.Helper()
	nextID++
	a := &app{t: t, id: common.NewApplicationId(nextID)}
	var p []byte
	if params != "" {
		p = []byte(params)
	}
	state, fuel, err := rt.Deploy(context.Background(), a.id, p, module)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if fuel == nil || fuel.Sign() <= 0 {
		t.Fatalf("deploy reported no fuel")
	}
	a.state = state
	return a
}

func decodeEvents(t *testing.T, evs []common.PlainEvent) []event {
	out := make([]event, 0, len(evs))
	for _, e := range evs {
		var m map[string]any
		if err := json.Unmarshal(e.Data, &m); err != nil {
			t.Fatalf("event data is not JSON: %q", e.Data)
		}
		out = append(out, event{User: strings.ToLower(e.UserID.Hex()), Raw: e.Data, Data: m})
	}
	return out
}

// commit applies what the Executor would sign: a new state only on success and under the limit.
func (a *app) commit(r *result) *result {
	if r.Failure == "" {
		if len(r.State) > maxStateSize {
			r.Failure = "APP_STATE_TOO_LARGE"
			return r
		}
		a.state = r.State
	}
	return r
}

func (a *app) deposit(sender, token, amount string) *result {
	a.t.Helper()
	v, _ := new(big.Int).SetString(amount, 10)
	start := time.Now()
	state, evs, appEvs, fuel, fail := rt.Deposit(context.Background(), a.id, ethCommon.HexToAddress(sender), ethCommon.HexToAddress(token), v, a.state, module)
	r := &result{Took: time.Since(start), Fuel: fuel, AppEvents: len(appEvs), State: state}
	if fail != nil {
		r.Failure = fail.Message
	} else {
		r.Events = decodeEvents(a.t, evs)
	}
	return a.commit(r)
}

func (a *app) request(kind common.RequestType, sender string, payload []byte) *result {
	a.t.Helper()
	start := time.Now()
	state, evs, appEvs, ws, report, fuel, fail := rt.ProcessRequest(context.Background(), a.id, ethCommon.HexToAddress(sender), kind, payload, a.state, module)
	r := &result{Took: time.Since(start), Fuel: fuel, AppEvents: len(appEvs), Withdrawals: ws, Report: report, State: state}
	if fail != nil {
		r.Failure = fail.Message
	} else {
		r.Events = decodeEvents(a.t, evs)
	}
	return a.commit(r)
}

func (a *app) send(sender string, payload map[string]any) *result {
	a.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		a.t.Fatal(err)
	}
	return a.request(common.Process, sender, b)
}

// ok: the request succeeded and was not refused by the app.
func (a *app) ok(sender string, payload map[string]any) *result {
	a.t.Helper()
	r := a.send(sender, payload)
	if r.Failure != "" {
		a.t.Fatalf("%v from %s failed: %s", payload, sender, r.Failure)
	}
	if code := rejected(r); code != "" {
		a.t.Fatalf("%v from %s was refused: %s", payload, sender, code)
	}
	return r
}

// rejected: under reject = private a refusal is a success whose event to the sender says why.
func rejected(r *result) string {
	for _, e := range r.Events {
		if c, ok := e.Data["rejected"].(string); ok {
			return c
		}
	}
	return ""
}

// the app's own view of the state (the adapter keeps its policy and padding beside it)
func (a *app) view() map[string]any {
	a.t.Helper()
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimRight(a.state, " \x00"), &m); err != nil {
		a.t.Fatalf("state is not JSON: %v", err)
	}
	return m
}

func (a *app) auction(id string) map[string]any {
	auctions, _ := a.view()["auctions"].(map[string]any)
	m, _ := auctions[id].(map[string]any)
	return m
}

func (a *app) balance(account, token string) string {
	balances, _ := a.view()["balances"].(map[string]any)
	row, ok := balances[account].(map[string]any)
	if !ok {
		return "0"
	}
	if v, ok := row[token].(string); ok {
		return v
	}
	return "0"
}

func auctionID(n int) string { return fmt.Sprintf("0x%032x", n) }

// 1000 TST with the seller; 2 ETH each with Ana, Bob and Carl; one auction open
func funded(t *testing.T) *app {
	a := deploy(t, "")
	for _, d := range [][3]string{{seller, tst, "1000000000"}, {ana, eth, "2000000000000000000"}, {bob, eth, "2000000000000000000"}, {carl, eth, "2000000000000000000"}} {
		if r := a.deposit(d[0], d[1], d[2]); r.Failure != "" {
			t.Fatalf("deposit: %s", r.Failure)
		}
	}
	return a
}

func opened(t *testing.T, kind string) *app {
	a := funded(t)
	a.ok(seller, map[string]any{"type": "open", "asset": tst, "quantity": "1000000000", "payment": eth, "reserve": "500000000000000000", "kind": kind})
	return a
}

// Ana 600 for 0.9 ETH, Bob 600 for 0.6 ETH, Carl 100 for 0.01 ETH (below the reserve)
func withBids(t *testing.T, kind string) *app {
	a := opened(t, kind)
	a.ok(ana, map[string]any{"type": "bid", "auction": id1, "quantity": "600000000", "total": "900000000000000000"})
	a.ok(bob, map[string]any{"type": "bid", "auction": id1, "quantity": "600000000", "total": "600000000000000000"})
	a.ok(carl, map[string]any{"type": "bid", "auction": id1, "quantity": "100000000", "total": "10000000000000000"})
	return a
}

func sizes(evs []event) []int {
	out := make([]int, len(evs))
	for i, e := range evs {
		out[i] = len(e.Raw)
	}
	return out
}

// ---- the tests ----

// DARKPOOL-02: deployed with no parameters, the app is already private: a refusal is a success,
// the reason goes encrypted to the sender, every event is padded and there are at least two.
func TestPrivateByDefault(t *testing.T) {
	a := opened(t, "uniform")
	r := a.send(seller, map[string]any{"type": "bid", "auction": id1, "quantity": "1", "total": "1"})
	if r.Failure != "" {
		t.Fatalf("a refused request failed publicly: %q", r.Failure)
	}
	if got := rejected(r); got != "seller_cannot_bid" {
		t.Fatalf("expected the encrypted refusal seller_cannot_bid, got %q", got)
	}
	for _, p := range []map[string]any{
		{"type": "bid", "auction": id1, "quantity": "600000000", "total": "900000000000000000"},
		{"type": "withdraw", "token": eth, "amount": "1"},
		{"type": "close", "auction": "0x00000000000000000000000000000009"},
	} {
		r := a.send(ana, p)
		if len(r.Events) < 2 {
			t.Errorf("%v: %d events, the policy asks for at least 2", p["type"], len(r.Events))
		}
		for _, n := range sizes(r.Events) {
			if n%256 != 0 {
				t.Errorf("%v: an event of %d bytes is not padded to 256", p["type"], n)
			}
		}
	}
}

// DARKPOOL-08: a malformed amount is the app's refusal, not a public runtime_error.
func TestMalformedAmounts(t *testing.T) {
	a := opened(t, "uniform")
	for _, bad := range []any{"1.5", "-1", "", "0x10", "1e18", 7, nil, map[string]any{"x": 1}} {
		r := a.send(ana, map[string]any{"type": "bid", "auction": id1, "quantity": bad, "total": "1"})
		if r.Failure != "" {
			t.Errorf("quantity %#v failed publicly: %s", bad, r.Failure)
		}
	}
	for _, payload := range []string{`[]`, `"bid"`, `{"type": 5}`, `{}`} {
		r := a.request(common.Process, ana, []byte(payload))
		if r.Failure != "" {
			t.Errorf("payload %s failed publicly: %s", payload, r.Failure)
		}
	}
}

// DARKPOOL-06: no trigger contract, so a trusted request is refused by name.
func TestTrustedRefused(t *testing.T) {
	a := opened(t, "uniform")
	r := a.request(common.TrustProcess, eth, []byte(`{"type":"withdraw","token":"`+eth+`","amount":"1"}`))
	if !strings.Contains(r.Failure, "no_trigger") {
		t.Fatalf("a trusted request should fail with no_trigger, got failure=%q events=%d", r.Failure, len(r.Events))
	}
}

// DARKPOOL-11: cancel_bid works inside the guest (a public map with private keys did not).
func TestCancelBid(t *testing.T) {
	a := opened(t, "uniform")
	a.ok(ana, map[string]any{"type": "bid", "auction": id1, "quantity": "600000000", "total": "900000000000000000"})
	a.ok(bob, map[string]any{"type": "bid", "auction": id1, "quantity": "600000000", "total": "600000000000000000"})
	a.ok(ana, map[string]any{"type": "cancel_bid", "auction": id1})
	if got := a.balance(ana, eth); got != "2000000000000000000" {
		t.Fatalf("Ana's escrow did not come back: %s", got)
	}
	bids, _ := a.auction(id1)["bids"].(map[string]any)
	if _, still := bids[ana]; still || len(bids) != 1 {
		t.Fatalf("the book after cancel_bid: %v", bids)
	}
}

// DARKPOOL-10: losers learn only their refund; nobody learns another bidder's bid; the seller
// learns how many won, and only the state (for the audit) lists the winners. Every event of a
// close has the same size, and their number is rounded up to a power of two.
func TestCloseRevealsNoBid(t *testing.T) {
	for _, kind := range []string{"uniform", "pay_as_bid"} {
		a := withBids(t, kind)
		r := a.ok(seller, map[string]any{"type": "close", "auction": id1})
		if len(r.Events) != 4 {
			t.Errorf("%s: %d events at a close with 3 bidders, want 4 (3 + the seller, a power of two)", kind, len(r.Events))
		}
		for _, e := range r.Events {
			for _, key := range []string{"clearing", "quantity", "total", "bids"} {
				if _, leak := e.Data[key]; leak {
					t.Errorf("%s: the event to %s carries %q: %s", kind, e.User, key, e.Raw)
				}
			}
			if e.Data["type"] == "cleared" {
				if _, named := e.Data["fills"]; named || e.Data["winners"] != "2" {
					t.Errorf("%s: the seller should learn how many won (2), not who: %s", kind, e.Raw)
				}
			}
			if e.User == carl && e.Data["type"] == "result" && e.Data["filled"] != "0" {
				t.Errorf("%s: Carl was below the reserve and got %v", kind, e.Data["filled"])
			}
		}
		sz := sizes(r.Events)
		for _, n := range sz {
			if n != sz[0] {
				t.Errorf("%s: event sizes differ at the close: %v", kind, sz)
				break
			}
		}
		au := a.auction(id1)
		if bids, _ := au["bids"].(map[string]any); len(bids) != 0 {
			t.Errorf("%s: the finished auction keeps its book: %d bids", kind, len(bids))
		}
		res, _ := au["result"].(map[string]any)
		if fills, _ := res["fills"].([]any); len(fills) != 2 {
			t.Errorf("%s: the stored result lists %d fills, want 2", kind, len(fills))
		}
	}
}

// Settlement is unchanged by any of the above (the numbers of the native tests).
func TestSettlement(t *testing.T) {
	a := withBids(t, "uniform")
	a.ok(seller, map[string]any{"type": "close", "auction": id1})
	for _, c := range [][3]string{{ana, tst, "600000000"}, {bob, tst, "400000000"}, {carl, tst, "0"}, {ana, eth, "1400000000000000000"}, {bob, eth, "1600000000000000000"}, {carl, eth, "2000000000000000000"}, {seller, eth, "1000000000000000000"}} {
		if got := a.balance(c[0], c[1]); got != c[2] {
			t.Errorf("balance of %s in %s: %s, want %s", c[0], c[1], got, c[2])
		}
	}
	r := a.ok(ana, map[string]any{"type": "withdraw", "token": tst, "amount": "600000000"})
	if len(r.Withdrawals) != 1 || r.Withdrawals[0].Amount.ToInt().String() != "600000000" {
		t.Fatalf("withdrawal: %+v", r.Withdrawals)
	}
	// DARKPOOL-01: an emptied balance leaves no row behind
	if balances, _ := a.view()["balances"].(map[string]any); balances[ana].(map[string]any)[tst] != nil {
		t.Errorf("Ana's emptied TST balance is still a row")
	}
}

// DARKPOOL-01: open auctions are capped per seller and in total.
func TestOpenCaps(t *testing.T) {
	a := deploy(t, "")
	open := func(who string) *result {
		return a.send(who, map[string]any{"type": "open", "asset": tst, "quantity": "1", "payment": eth})
	}
	sellers := []string{}
	for i := 0; i < 8; i++ {
		s := fmt.Sprintf("0x%040x", 0x5000+i)
		sellers = append(sellers, s)
		a.deposit(s, tst, "100")
	}
	for i := 0; i < 3; i++ {
		if c := rejected(open(sellers[0])); c != "" {
			t.Fatalf("open %d of seller 0 refused: %s", i+1, c)
		}
	}
	if c := rejected(open(sellers[0])); c != "too_many_open" {
		t.Fatalf("the 4th open auction of one seller: %q, want too_many_open", c)
	}
	opened := 3
	for _, s := range sellers[1:] {
		for i := 0; i < 3 && opened < 20; i++ {
			if c := rejected(open(s)); c != "" {
				t.Fatalf("open #%d refused: %s", opened+1, c)
			}
			opened++
		}
	}
	if c := rejected(open(sellers[7])); c != "too_many_open" {
		t.Fatalf("the 21st open auction: %q, want too_many_open", c)
	}
}

// DARKPOOL-01: the history is pruned on close and cancel too, and the state stops growing.
func TestHistoryBounded(t *testing.T) {
	a := funded(t)
	a.deposit(seller, tst, "1000")
	var sizesAt []int
	for i := 1; i <= 70; i++ {
		id := auctionID(i)
		a.ok(seller, map[string]any{"type": "open", "asset": tst, "quantity": "10", "payment": eth})
		a.ok(ana, map[string]any{"type": "bid", "auction": id, "quantity": "10", "total": "1000"})
		if i%2 == 0 {
			a.ok(seller, map[string]any{"type": "close", "auction": id})
		} else {
			a.ok(seller, map[string]any{"type": "cancel", "auction": id})
		}
		if i == 50 || i == 70 {
			sizesAt = append(sizesAt, len(a.state))
		}
	}
	auctions, _ := a.view()["auctions"].(map[string]any)
	if len(auctions) > 50 {
		t.Errorf("%d finished auctions kept, want at most 50", len(auctions))
	}
	if sizesAt[1] > sizesAt[0]+512 {
		t.Errorf("the state kept growing past the cap: %d bytes at 50 auctions, %d at 70", sizesAt[0], sizesAt[1])
	}
	t.Logf("state after 70 finished auctions: %d bytes", sizesAt[1])
}

// DARKPOOL-01, the worst case: 20 full books of 200 bids, plus a full history, stay far below the
// limit and every close still fits. Slow (8000 requests): DARKPOOL_FULL=1.
func TestWorstCaseState(t *testing.T) {
	if os.Getenv("DARKPOOL_FULL") == "" {
		t.Skip("DARKPOOL_FULL=1 to fill 20 books of 200 bids")
	}
	a := deploy(t, "")
	for s := 0; s < 7; s++ {
		a.deposit(fmt.Sprintf("0x%040x", 0x5000+s), tst, "3000000000")
	}
	for b := 0; b < 200; b++ {
		a.deposit(fmt.Sprintf("0x%040x", 0x9000+b), eth, "1000000000000000000000")
	}
	for i := 0; i < 20; i++ {
		a.ok(fmt.Sprintf("0x%040x", 0x5000+i/3), map[string]any{"type": "open", "asset": tst, "quantity": "1000000000", "payment": eth})
	}
	var slowest time.Duration
	for i := 1; i <= 20; i++ {
		for b := 0; b < 200; b++ {
			r := a.ok(fmt.Sprintf("0x%040x", 0x9000+b), map[string]any{"type": "bid", "auction": auctionID(i), "quantity": fmt.Sprint(1000000 + b), "total": fmt.Sprint(999999999999999 - b)})
			if r.Took > slowest {
				slowest = r.Took
			}
		}
		t.Logf("book %d full: state %d bytes, slowest call so far %v", i, len(a.state), slowest)
	}
	full := len(a.state)
	r := a.ok(fmt.Sprintf("0x%040x", 0x5000), map[string]any{"type": "close", "auction": auctionID(1)})
	t.Logf("20 full books: %d bytes (limit %d); a 200-bid close took %v", full, maxStateSize, r.Took)
	if full > maxStateSize/4 {
		t.Errorf("the worst case uses %d bytes, more than a quarter of the limit", full)
	}
}

// DARKPOOL-09: the authority's report holds the balances and the finished auctions' results, and
// never an open book.
func TestAuditReport(t *testing.T) {
	a := withBids(t, "uniform")
	a.ok(seller, map[string]any{"type": "close", "auction": id1})
	a.deposit(seller, tst, "50")
	a.ok(seller, map[string]any{"type": "open", "asset": tst, "quantity": "50", "payment": eth})
	a.ok(ana, map[string]any{"type": "bid", "auction": auctionID(2), "quantity": "50", "total": "123456789"})
	r := a.request(common.Deanonymize, eth, []byte(`{"report_type":"audit"}`))
	if r.Failure != "" {
		t.Fatalf("audit: %s", r.Failure)
	}
	if bytes.Contains(r.Report, []byte("123456789")) {
		t.Errorf("the audit report shows a bid of an open auction: %s", r.Report)
	}
	if !bytes.Contains(r.Report, []byte(`"sold"`)) {
		t.Errorf("the audit report lacks the finished auction's result: %s", r.Report)
	}
}

// The same request on the same state gives the same bytes: the Executor signs a state root.
func TestDeterministic(t *testing.T) {
	a := withBids(t, "uniform")
	before := append([]byte(nil), a.state...)
	r1 := a.send(seller, map[string]any{"type": "close", "auction": id1})
	a.state = before
	r2 := a.send(seller, map[string]any{"type": "close", "auction": id1})
	if !bytes.Equal(r1.State, r2.State) {
		t.Fatal("two runs of the same close gave different states")
	}
	for i := range r1.Events {
		if !bytes.Equal(r1.Events[i].Raw, r2.Events[i].Raw) {
			t.Fatalf("event %d differs between two runs", i)
		}
	}
	if r1.Fuel.Cmp(r2.Fuel) != 0 {
		t.Fatal("the fuel differs between two runs")
	}
}
