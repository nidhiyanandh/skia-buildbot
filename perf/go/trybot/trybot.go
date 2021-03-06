/** trybot is for loading and serving trybot performance results.

  Implementation notes:

    Regular files are in:

    gs://chromium-skia-gm/nano-json-v1/2014/08/07/01/Perf-...-Release/nanobench_da7a94...8d35a_1407357280.json

    while trybots are in:

    gs://chromium-skia-gm/trybot/nano-json-v1/2014/08/07/05/Perf-...-Release-Trybot/85/448043002/nanobench_da7a944...d35a_1407357280.json

    Note the 'trybot' dir prefix and the addition of the build number and codereview issue number in the directory.
    The Rietveld issue id appears before the file name.  Note that some of the tries aren't associated with an issue, we will ignore those.


    Notes: I tried using both GOB and JSON as the serialization format and got the following numbers:

       GOB:  40s 12MB  49s (second run)
       JSON: 43s 14MB  43s (second run)

    Since there isn't a material difference between the two let's go with JSON
    as that's a little easier to debug.
*/

package trybot

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"

	metrics "github.com/rcrowley/go-metrics"
	"github.com/skia-dev/glog"

	storage "code.google.com/p/google-api-go-client/storage/v1"

	"go.skia.org/infra/go/util"
	"go.skia.org/infra/perf/go/db"
	"go.skia.org/infra/perf/go/ingester"
	"go.skia.org/infra/perf/go/types"
)

var (
	// nameRegex is the regexp that a trybot filename must match. This enforces the need for a Rietveld issue number.
	//
	// REPL here: http://play.golang.org/p/uGmexyFxEr
	nameRegex = regexp.MustCompile(`trybot/nano-json-v1/\d{4}/\d{2}/\d{2}/\d{2}/[^/]+/\d+/(\d+)/(.*)`)

	st *storage.Service = nil
)

// Write the TryBotResults to the datastore.
func Write(issue string, try *types.TryBotResults) error {
	b, err := json.Marshal(try)
	if err != nil {
		return fmt.Errorf("Failed to encode to JSON: %s", err)
	}
	glog.Infof("Writing: %s", issue)
	_, err = db.DB.Exec("REPLACE INTO tries (issue, results, lastUpdated) VALUES (?, ?, ?)", issue, b, time.Now())
	if err != nil {
		return fmt.Errorf("Failed to write to database: %s", err)
	}
	return nil
}

// Get the TryBotResults from the datastore.
func Get(issue string) (*types.TryBotResults, error) {
	var results string
	err := db.DB.QueryRow("SELECT results FROM tries WHERE issue=?", issue).Scan(&results)
	if err == sql.ErrNoRows {
		return types.NewTryBotResults(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("Failed to load try data with id %s: %s", issue, err)
	}
	try := &types.TryBotResults{}
	if err := json.Unmarshal([]byte(results), try); err != nil {
		return nil, fmt.Errorf("Failed to decode try data with id: %s", issue)
	}
	return try, nil
}

// List returns the last N Rietveld issue IDs.
func List(n int) ([]string, error) {
	rows, err := db.DB.Query("SELECT issue FROM tries ORDER BY lastUpdated DESC LIMIT ?", n)
	if err != nil {
		return nil, fmt.Errorf("Failed to read try data from database: %s", err)
	}
	defer util.Close(rows)

	ret := []string{}
	for rows.Next() {
		var issue string
		if err := rows.Scan(&issue); err != nil {
			return nil, fmt.Errorf("List: Failed to read issus from row: %s", err)
		}
		ret = append(ret, issue)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ret)))
	return ret, nil
}

// TileWithTryData will add all the trybot data for the given issue to the
// given Tile. A new Tile that is a copy of the original Tile will be returned,
// so we aren't modifying the underlying Tile.
func TileWithTryData(tile *types.Tile, issue string) (*types.Tile, error) {
	ret := tile.Copy()
	lastCommitIndex := tile.LastCommitIndex()
	// The way we handle Tiles there is always empty space at the end of the
	// Tile of index -1. Use that space to inject the trybot results.
	ret.Commits[lastCommitIndex+1].CommitTime = time.Now().Unix()
	lastCommitIndex = ret.LastCommitIndex()

	tryResults, err := Get(issue)
	if err != nil {
		return nil, fmt.Errorf("AppendToTile: Failed to retreive trybot results: %s", err)
	}
	// Copy in the trybot data.
	for k, v := range tryResults.Values {
		if tr, ok := ret.Traces[k]; !ok {
			continue
		} else {
			tr.(*types.PerfTrace).Values[lastCommitIndex] = v
		}
	}
	return ret, nil
}

// addTryData copies the data from the ResultsFileLocation into the TryBotResults.
func addTryData(res *types.TryBotResults, opener ingester.Opener, counter metrics.Counter) {
	r, err := opener()
	if err != nil {
		glog.Errorf("Error opening input reader: %s", err)
	}

	benchData, err := ingester.ParseBenchDataFromReader(r)
	if err != nil {
		// Don't fall over for a single corrupt file.
		glog.Errorf("Unable to parse trybot data: %s", err)
		return
	}

	// cb is the anonymous closure we'll pass over all the trace values found in benchData.
	cb := func(key string, value float64, params map[string]string) {
		res.Values[key] = value
		counter.Inc(1)
	}

	benchData.ForEach(cb)
}

// BenchByIssue allows sorting ResultsFileLocation's by the Rietveld issue id.
//
// We sort on issue id so that we aren't doing excessive writes to the
// database.
type BenchByIssue struct {
	opener    ingester.Opener
	IssueName string
}

type BenchByIssueSlice []*BenchByIssue

func (p BenchByIssueSlice) Len() int           { return len(p) }
func (p BenchByIssueSlice) Less(i, j int) bool { return p[i].IssueName < p[j].IssueName }
func (p BenchByIssueSlice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

func init() {
	var err error
	st, err = storage.New(util.NewTimeoutClient())
	if err != nil {
		panic("Can't construct HTTP client")
	}

	ingester.Register("nano-trybot", NewTrybotResultIngester)
}

// TrybotResultIngester implements the ingester.ResultIngester interface.
type TrybotResultIngester struct {
	benchFilesByIssue []*BenchByIssue
}

func NewTrybotResultIngester() ingester.ResultIngester {
	return &TrybotResultIngester{
		benchFilesByIssue: []*BenchByIssue{},
	}
}

// See the ingester.ResultIngester interface.
func (i *TrybotResultIngester) Ingest(_ *ingester.TileTracker, opener ingester.Opener, fname string, counter metrics.Counter) error {
	match := nameRegex.FindStringSubmatch(fname)
	if match != nil {
		issue := match[1]
		i.benchFilesByIssue = append(i.benchFilesByIssue, &BenchByIssue{
			opener:    opener,
			IssueName: issue,
		})
	}
	return nil
}

// See the ingester.ResultIngester interface.
func (i *TrybotResultIngester) BatchFinished(counter metrics.Counter) error {
	// Reset this instance regardless of the outcome of this call.
	defer func() {
		i.benchFilesByIssue = []*BenchByIssue{}
	}()

	// Resort by issue id.
	sort.Sort(BenchByIssueSlice(i.benchFilesByIssue))

	lastIssue := ""
	var cur *types.TryBotResults = nil
	var err error
	for _, b := range i.benchFilesByIssue {
		if b.IssueName != lastIssue {
			// Write out the current TryBotResults to the datastore and create a fresh new TryBotResults.
			if cur != nil {
				if err := Write(lastIssue, cur); err != nil {
					return fmt.Errorf("Update failed to write trybot results: %s", err)
				}
			}
			if cur, err = Get(b.IssueName); err != nil {
				return fmt.Errorf("Failed to load existing trybot data for issue %s: %s", b.IssueName, err)
			}
			lastIssue = b.IssueName
			glog.Infof("Switched to issue: %s", lastIssue)
		}
		addTryData(cur, b.opener, counter)
	}
	if cur != nil {
		if err := Write(lastIssue, cur); err != nil {
			return fmt.Errorf("Update failed to write trybot results: %s", err)
		}
	}

	glog.Infof("Finished trybot ingestion.")
	return nil
}
