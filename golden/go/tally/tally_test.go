package tally

import (
	"net/url"
	"testing"

	gtypes "go.skia.org/infra/golden/go/types"
	"go.skia.org/infra/perf/go/types"
)

func TestTallyBasic(t *testing.T) {
	// Create a tile to test against.
	tile := types.NewTile()
	trace1 := types.NewGoldenTrace()
	trace1.Values[0] = "aaa"
	trace1.Values[1] = "aaa"
	trace1.Values[2] = "bbb"
	trace1.Params_[gtypes.PRIMARY_KEY_FIELD] = "foo"
	trace1.Params_["corpus"] = "gm"
	tile.Traces["foo:x86"] = trace1

	trace2 := types.NewGoldenTrace()
	trace2.Values[0] = "ccc"
	trace2.Values[1] = "aaa"
	trace2.Params_[gtypes.PRIMARY_KEY_FIELD] = "foo"
	trace2.Params_["corpus"] = "image"
	tile.Traces["foo:x86_64"] = trace2

	// Test tallyTile with our Tile.
	trace, test := tallyTile(tile)
	if got, want := len(trace), 2; got != want {
		t.Errorf("Wrong trace count: Got %v Want %v", got, want)
	}
	if got, want := (*trace["foo:x86"])["aaa"], 2; got != want {
		t.Errorf("Miscount: Got %v Want %v", got, want)
	}

	if got, want := len(test), 1; got != want {
		t.Errorf("Wrong trace count: Got %v Want %v", got, want)
	}
	if got, want := (*test["foo"])["aaa"], 3; got != want {
		t.Errorf("Miscount: Got %v Want %v", got, want)
	}
	if got, want := (*test["foo"])["bbb"], 1; got != want {
		t.Errorf("Miscount: Got %v Want %v", got, want)
	}
	if got, want := (*test["foo"])["ccc"], 1; got != want {
		t.Errorf("Miscount: Got %v Want %v", got, want)
	}

	// Test tallyBy with our Tile.
	ta := tallyBy(tile, trace, url.Values{"corpus": []string{"gm"}})
	if got, want := len(ta), 2; got != want {
		t.Errorf("Wrong trace count: Got %v Want %v", got, want)
	}
	if got, want := ta["aaa"], 2; got != want {
		t.Errorf("Miscount: Got %v Want %v", got, want)
	}
	if got, want := ta["bbb"], 1; got != want {
		t.Errorf("Miscount: Got %v Want %v", got, want)
	}
}
