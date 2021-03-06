package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"text/template"
	"time"

	"github.com/gorilla/mux"
	"github.com/skia-dev/glog"
	"go.skia.org/infra/go/login"
	"go.skia.org/infra/go/timer"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/golden/go/diff"
	"go.skia.org/infra/golden/go/summary"
	"go.skia.org/infra/golden/go/types"
)

var (
	// indexTemplate is the main index.html page we serve.
	indexTemplate *template.Template = nil

	// ignoresTemplate is the page for setting up ignore filters.
	ignoresTemplate *template.Template = nil

	// compareTemplate is the page for setting up ignore filters.
	compareTemplate *template.Template = nil

	// singleTemplate is the page for viewing a single digest.
	singleTemplate *template.Template = nil

	// helpTemplate is the help page.
	helpTemplate *template.Template = nil

	// triageLogTemplate renders the triage changes listing.
	triageLogTemplate *template.Template = nil
)

// polyMainHandler is the main page for the Polymer based frontend.
func polyMainHandler(w http.ResponseWriter, r *http.Request) {
	glog.Infof("Poly Main Handler: %q\n", r.URL.Path)
	w.Header().Set("Content-Type", "text/html")
	if *local {
		loadTemplates()
	}
	if err := indexTemplate.Execute(w, struct{}{}); err != nil {
		glog.Errorln("Failed to expand template:", err)
	}
}

func loadTemplates() {
	indexTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/index.html"),
		filepath.Join(*resourcesDir, "templates/titlebar.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
	ignoresTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/ignores.html"),
		filepath.Join(*resourcesDir, "templates/titlebar.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
	compareTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/compare.html"),
		filepath.Join(*resourcesDir, "templates/titlebar.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))

	singleTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/single.html"),
		filepath.Join(*resourcesDir, "templates/titlebar.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))

	helpTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/help.html"),
		filepath.Join(*resourcesDir, "templates/titlebar.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))

	triageLogTemplate = template.Must(template.ParseFiles(
		filepath.Join(*resourcesDir, "templates/triagelog.html"),
		filepath.Join(*resourcesDir, "templates/titlebar.html"),
		filepath.Join(*resourcesDir, "templates/header.html"),
	))
}

type SummarySlice []*summary.Summary

func (p SummarySlice) Len() int           { return len(p) }
func (p SummarySlice) Less(i, j int) bool { return p[i].Name < p[j].Name }
func (p SummarySlice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

// polyListTestsHandler returns a JSON list with high level information about
// each test.
//
// Takes two query parameters:
//  include - True if ignored digests should be included. (true, false)
//  query   - A query to restrict the responses to, encoded as a URL encoded paramset.
//
// The return format looks like:
//
//  [
//    {
//      "name": "01-original",
//      "diameter": 123242,
//      "untriaged": 2,
//      "num": 2
//    },
//    ...
//  ]
//
func polyListTestsHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		util.ReportError(w, r, err, "Failed to parse form data.")
		return
	}
	// If the query only includes source_type parameters, and include==false, then we can just
	// filter the response from summaries.Get(). If the query is broader than that, or
	// include==true, then we need to call summary.CalcSummaries().
	if err := r.ParseForm(); err != nil {
		util.ReportError(w, r, err, "Invalid request.")
		return
	}
	q, err := url.ParseQuery(r.FormValue("query"))
	if err != nil {
		util.ReportError(w, r, err, "Invalid query in request.")
	}
	_, hasSourceType := q["source_type"]
	sumSlice := []*summary.Summary{}
	if r.FormValue("include") == "false" && len(q) == 1 && hasSourceType {
		sumMap := summaries.Get()
		corpus := q["source_type"]
		for _, s := range sumMap {
			if util.In(s.Corpus, corpus) {
				sumSlice = append(sumSlice, s)
			}
		}
	} else {
		glog.Infof("%q %q", r.FormValue("query"), r.FormValue("include"))
		sumMap, err := summary.CalcSummaries(tileStore, expStore, tallies, diffStore, ignoreStore, nil, r.FormValue("query"), r.FormValue("include") == "true")
		if err != nil {
			util.ReportError(w, r, err, "Failed to calculate summaries.")
		}
		for _, s := range sumMap {
			sumSlice = append(sumSlice, s)
		}
	}
	sort.Sort(SummarySlice(sumSlice))
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(sumSlice); err != nil {
		util.ReportError(w, r, err, "Failed to encode result")
	}
}

// polyTestStatusHandler returns the status of the requested test.
func polyTestStatusHandler(w http.ResponseWriter, r *http.Request) {
	test := mux.Vars(r)["test"]
	var summary *summary.Summary
	var ok bool
	if summary, ok = summaries.Get()[test]; !ok {
		util.ReportError(w, r, fmt.Errorf("Unknown test: %q", test), "No summaries for test.")
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(summary); err != nil {
		util.ReportError(w, r, err, "Failed to encode result")
	}
}

// polyIgnoresJSONHandler returns the current ignore rules in JSON format.
func polyIgnoresJSONHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	ignores := []*types.IgnoreRule{}
	var err error
	if *startAnalyzer {
		ignores, err = analyzer.ListIgnoreRules()
	} else {
		ignores, err = ignoreStore.List()
	}
	if err != nil {
		util.ReportError(w, r, err, "Failed to retrieve ignored traces.")
	}

	// TODO(stephana): Wrap in response envelope if it makes sense !
	enc := json.NewEncoder(w)
	if err := enc.Encode(ignores); err != nil {
		util.ReportError(w, r, err, "Failed to encode result")
	}
}

func polyIgnoresDeleteHandler(w http.ResponseWriter, r *http.Request) {
	user := login.LoggedInAs(r)
	if user == "" {
		util.ReportError(w, r, fmt.Errorf("Not logged in."), "You must be logged in to add an ignore rule.")
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 0)
	if err != nil {
		util.ReportError(w, r, err, "ID must be valid integer.")
		return
	}

	if *startAnalyzer {
		err = analyzer.DeleteIgnoreRule(int(id), user)
	} else {
		_, err = ignoreStore.Delete(int(id), user)
	}

	if err != nil {
		util.ReportError(w, r, err, "Unable to delete ignore rule.")
	} else {
		// If delete worked just list the current ignores and return them.
		polyIgnoresJSONHandler(w, r)
	}
}

type IgnoresAddRequest struct {
	Duration string `json:"duration"`
	Filter   string `json:"filter"`
	Note     string `json:"note"`
}

var durationRe = regexp.MustCompile("([0-9]+)([smhdw])")

// polyIgnoresAddHandler is for adding a new ignore rule.
func polyIgnoresAddHandler(w http.ResponseWriter, r *http.Request) {
	user := login.LoggedInAs(r)
	if user == "" {
		util.ReportError(w, r, fmt.Errorf("Not logged in."), "You must be logged in to add an ignore rule.")
		return
	}
	req := &IgnoresAddRequest{}
	if err := parseJson(r, req); err != nil {
		util.ReportError(w, r, err, "Failed to parse submitted data.")
		return
	}
	parsed := durationRe.FindStringSubmatch(req.Duration)
	if len(parsed) != 3 {
		util.ReportError(w, r, fmt.Errorf("Rejected duration: %s", req.Duration), "Failed to parse duration")
		return
	}
	// TODO break out the following into its own func, add tests.
	n, err := strconv.ParseInt(parsed[1], 10, 32)
	if err != nil {
		util.ReportError(w, r, err, "Failed to parse duration")
		return
	}
	d := time.Second
	switch parsed[2][0] {
	case 's':
		d = time.Duration(n) * time.Second
	case 'm':
		d = time.Duration(n) * time.Minute
	case 'h':
		d = time.Duration(n) * time.Hour
	case 'd':
		d = time.Duration(n) * 24 * time.Hour
	case 'w':
		d = time.Duration(n) * 7 * 24 * time.Hour
	}
	ignoreRule := types.NewIgnoreRule(user, time.Now().Add(d), req.Filter, req.Note)
	if err != nil {
		util.ReportError(w, r, err, "Failed to create ignore rule.")
		return
	}

	if *startAnalyzer {
		err = analyzer.AddIgnoreRule(ignoreRule)
	} else {
		err = ignoreStore.Create(ignoreRule)
	}
	if err != nil {
		util.ReportError(w, r, err, "Failed to create ignore rule.")
		return
	}

	polyIgnoresJSONHandler(w, r)
}

// polyIgnoresHandler is for setting up ignores rules.
func polyIgnoresHandler(w http.ResponseWriter, r *http.Request) {
	glog.Infof("Poly Ignores Handler: %q\n", r.URL.Path)
	w.Header().Set("Content-Type", "text/html")
	if *local {
		loadTemplates()
	}
	if err := ignoresTemplate.Execute(w, struct{}{}); err != nil {
		glog.Errorln("Failed to expand template:", err)
	}
}

// polySingleDigestHandler is a page about a single digest.
func polySingleDigestHandler(w http.ResponseWriter, r *http.Request) {
	glog.Infof("Poly Single Digest Handler: %q\n", r.URL.Path)
	w.Header().Set("Content-Type", "text/html")
	if *local {
		loadTemplates()
	}
	if err := singleTemplate.Execute(w, struct{}{}); err != nil {
		glog.Errorln("Failed to expand template:", err)
	}
}

// polyHelpHandler is for serving the main compare page.
func polyHelpHandler(w http.ResponseWriter, r *http.Request) {
	glog.Infof("Poly Help Handler: %q\n", r.URL.Path)

	w.Header().Set("Content-Type", "text/html")
	if *local {
		loadTemplates()
	}
	if err := helpTemplate.Execute(w, struct{}{}); err != nil {
		glog.Errorln("Failed to expand template:", err)
	}
}

// polyCompareHandler is for serving the main compare page.
func polyCompareHandler(w http.ResponseWriter, r *http.Request) {
	glog.Infof("Poly Compare Handler: %q\n", r.URL.Path)
	w.Header().Set("Content-Type", "text/html")
	if *local {
		loadTemplates()
	}
	if err := compareTemplate.Execute(w, struct{}{}); err != nil {
		glog.Errorln("Failed to expand template:", err)
	}
}

// polyTriageLogView is for serving the main triage log page.
func polyTriageLogView(w http.ResponseWriter, r *http.Request) {
	glog.Infof("Poly Triagelog Handler: %q\n", r.URL.Path)
	w.Header().Set("Content-Type", "text/html")
	if *local {
		loadTemplates()
	}
	if err := triageLogTemplate.Execute(w, struct{}{}); err != nil {
		glog.Errorln("Failed to expand template:", err)
	}
}

// TODO(stephana): Refactor when moving to actual log entries.
type GUITriageLogEntry struct {
	Name        string `json:"name"`
	TS          int64  `json:"ts"`
	ChangeCount int    `json:"changeCount"`
}

// polyTriageLogHandler returns the entries in the triagelog paginated
// in reverse chronological order.
func polyTriageLogHandler(w http.ResponseWriter, r *http.Request) {
	// TODO(stephana): Replace the mock data below with the actual triage log
	// data.
	logEntries := make([]interface{}, 100)
	defaultSize := 10
	maxSize := 20

	now := time.Now()
	for idx := range logEntries {
		logEntries[idx] = &GUITriageLogEntry{
			Name:        fmt.Sprintf("John Doe %d", idx),
			TS:          now.Add(time.Duration(idx) * time.Minute).Unix(),
			ChangeCount: 10 + idx,
		}
	}

	total := len(logEntries)
	var size int
	var offset int
	var err error
	query := r.URL.Query()
	size, err = strconv.Atoi(query.Get("size"))
	if err != nil {
		size = defaultSize
	}
	offset, err = strconv.Atoi(query.Get("offset"))
	if err != nil {
		offset = 0
	}

	if size < 1 {
		size = 1
	} else if size > maxSize {
		size = maxSize
	}

	if (offset < 0) || (offset >= total) {
		offset = 0
	}

	result := logEntries[offset : offset+size]
	pagination := &ResponsePagination{
		Offset: offset,
		Size:   size,
		Total:  total,
	}

	sendResponse(w, result, http.StatusOK, pagination)
}

// PolyTestRequest is the POST'd request body handled by polyTestHandler.
type PolyTestRequest struct {
	Test               string `json:"test"`
	TopFilter          string `json:"topFilter"`
	LeftFilter         string `json:"LeftFilter"`
	TopQuery           string `json:"topQuery"`
	LeftQuery          string `json:"leftQuery"`
	TopIncludeIgnores  bool   `json:"topIncludeIgnores"`
	LeftIncludeIgnores bool   `json:"leftIncludeIgnores"`
	TopN               int    `json:"topN"`
	LeftN              int    `json:"leftN"`
	Sort               string `json:"sort"`   // Which side to sort, "top" or "left".
	Dir                string `json:"dir"`    // Direction to sort, ["", "asc", "desc"]
	Digest             string `json:"digest"` // The digest to sort against.
}

// PolyTestImgInfo info about a single source digest. Used in PolyTestGUI.
type PolyTestImgInfo struct {
	Thumb            string  `json:"thumb"`
	Digest           string  `json:"digest"`
	N                int     `json:"n"`    // The number of images with this digest.
	PixelDiffPercent float32 `json:"diff"` // Diff from the given digest to compare against, otherwise zero.
}

// PolyTestImgInfoSlice is for sorting slices of PolyTestImgInfo.
type PolyTestImgInfoSlice []*PolyTestImgInfo

func (p PolyTestImgInfoSlice) Len() int { return len(p) }
func (p PolyTestImgInfoSlice) Less(i, j int) bool {
	if p[i].N != p[j].N {
		return p[i].N > p[j].N
	} else {
		return p[i].Digest > p[j].Digest
	}
}
func (p PolyTestImgInfoSlice) Swap(i, j int) { p[i], p[j] = p[j], p[i] }

// PolyTestImgInfoDiffAscSlice is for sorting slices of PolyTestImgInfo by PixelDiffPercent.
type PolyTestImgInfoDiffAscSlice []*PolyTestImgInfo

func (p PolyTestImgInfoDiffAscSlice) Len() int { return len(p) }
func (p PolyTestImgInfoDiffAscSlice) Less(i, j int) bool {
	if p[i].PixelDiffPercent != p[j].PixelDiffPercent {
		return p[i].PixelDiffPercent < p[j].PixelDiffPercent
	} else {
		return p[i].Digest < p[j].Digest
	}
}
func (p PolyTestImgInfoDiffAscSlice) Swap(i, j int) { p[i], p[j] = p[j], p[i] }

// PolyTestImgInfo info about a single diff between two source digests. Used in
// PolyTestGUI.
type PolyTestDiffInfo struct {
	Test             string  `json:"test"`
	Thumb            string  `json:"thumb"`
	TopDigest        string  `json:"topDigest"`
	LeftDigest       string  `json:"leftDigest"`
	NumDiffPixels    int     `json:"numDiffPixels"`
	PixelDiffPercent float32 `json:"pixelDiffPercent"`
	MaxRGBADiffs     []int   `json:"maxRGBADiffs"`
	DiffImg          string  `json:"diffImgUrl"`
	TopImg           string  `json:"topImgUrl"`
	LeftImg          string  `json:"leftImgUrl"`
}

// PolyTestGUI serialized as JSON is the response body from polyTestHandler.
type PolyTestGUI struct {
	Top       []*PolyTestImgInfo    `json:"top"`
	Left      []*PolyTestImgInfo    `json:"left"`
	Grid      [][]*PolyTestDiffInfo `json:"grid"`
	Message   string                `json:"message"`
	TopTotal  int                   `json:"topTotal"`
	LeftTotal int                   `json:"leftTotal"`
}

// imgInfo returns a populated slice of PolyTestImgInfo based on the filter and
// queryString passed in.
//
// max maybe set to -1, which means to not truncate the response digest slice.
// If sortAgainstHash is true then the result will be sorted in direction 'dir' versus the given 'digest',
// otherwise the results will be sorted in terms of ascending N.
func imgInfo(filter, queryString, testName string, e types.TestClassification, max int, includeIgnores bool, ignores []url.Values, sortAgainstHash bool, dir string, digest string) ([]*PolyTestImgInfo, int, error) {
	query, err := url.ParseQuery(queryString)
	if err != nil {
		return nil, 0, fmt.Errorf("Failed to parse Query in imgInfo: %s", err)
	}
	query["name"] = []string{testName}

	t := timer.New("tallies.ByQuery")
	if includeIgnores {
		ignores = []url.Values{}
	}
	digests, err := tallies.ByQuery(query, ignores...)
	if err != nil {
		return nil, 0, fmt.Errorf("Failed to retrieve tallies in imgInfo: %s", err)
	}
	glog.Infof("Num Digests: %d", len(digests))
	t.Stop()

	// If we are going to sort against a digest then we need to calculate
	// the diff metrics against that digest.
	diffMetrics := map[string]*diff.DiffMetrics{}
	if sortAgainstHash {
		digestSlice := make([]string, len(digests))
		for d, _ := range digests {
			digestSlice = append(digestSlice, d)
		}
		var err error
		diffMetrics, err = diffStore.Get(digest, digestSlice)
		if err != nil {
			return nil, 0, fmt.Errorf("Failed to calculate diffs to sort against: %s", err)
		}
	}

	label := types.LabelFromString(filter)
	// Now filter digests by their expectations status here.
	t = timer.New("apply expectations")
	ret := []*PolyTestImgInfo{}
	for digest, n := range digests {
		if e[digest] != label {
			continue
		}
		p := &PolyTestImgInfo{
			Digest: digest,
			N:      n,
		}
		if sortAgainstHash {
			p.PixelDiffPercent = diffMetrics[digest].PixelDiffPercent
		}
		ret = append(ret, p)
	}
	t.Stop()

	if sortAgainstHash {
		if dir == "asc" {
			sort.Sort(PolyTestImgInfoDiffAscSlice(ret))
		} else {
			sort.Sort(sort.Reverse(PolyTestImgInfoDiffAscSlice(ret)))
		}
	} else {
		sort.Sort(PolyTestImgInfoSlice(ret))
	}

	total := len(ret)
	if max > 0 && len(ret) > max {
		ret = ret[:max]
	}
	return ret, total, nil
}

// polyTestHandler returns a JSON description for the given test.
//
// Takes an JSON encoded POST body of the following form:
//
//   {
//      test: The name of the test.
//      topFilter=["positive", "negative", "untriaged"]
//      leftFilter=["positive", "negative", "untriaged"]
//      topQuery: "",
//      leftQuery: "",
//      topIncludeIgnores: bool,
//      leftIncludeIgnores: bool,
//      topN: topN,
//      leftN: leftN,
//   }
//
//
// The return format looks like:
//
// {
//   "top": [img1, img2, ...]
//   "left": [imgA, imgB, ...]
//   "grid": [
//     [diff1A, diff2A, ...],
//     [diff1B, diff2B, ...],
//   ],
//   info: "Could be error or warning.",
// }
//
// Where imgN is serialized PolyTestImgInfo, and
//       diffN is a serialized PolyTestDiffInfo struct.
// Note that this format is what res/imp/grid expects to
// receive.
//
func polyTestHandler(w http.ResponseWriter, r *http.Request) {
	req := &PolyTestRequest{}
	if err := parseJson(r, req); err != nil {
		util.ReportError(w, r, err, "Failed to parse JSON request.")
		return
	}
	exp, err := expStore.Get()
	if err != nil {
		util.ReportError(w, r, err, "Failed to load expectations.")
		return
	}
	e := exp.Tests[req.Test]

	ignores := []url.Values{}

	allIgnores, err := ignoreStore.List()
	if err != nil {
		util.ReportError(w, r, err, "Failed to load ignore rules.")
		return
	}
	for _, i := range allIgnores {
		q, _ := url.ParseQuery(i.Query)
		ignores = append(ignores, q)
	}

	topDigests, topTotal, err := imgInfo(req.TopFilter, req.TopQuery, req.Test, e, req.TopN, req.TopIncludeIgnores, ignores, req.Sort == "top", req.Dir, req.Digest)
	leftDigests, leftTotal, err := imgInfo(req.LeftFilter, req.LeftQuery, req.Test, e, req.LeftN, req.LeftIncludeIgnores, ignores, req.Sort == "left", req.Dir, req.Digest)

	// Extract out string slices of digests to pass to *AbsPath and diffStore.Get().
	allDigests := map[string]bool{}
	topDigestMap := map[string]bool{}
	for _, d := range topDigests {
		allDigests[d.Digest] = true
		topDigestMap[d.Digest] = true
	}
	for _, d := range leftDigests {
		allDigests[d.Digest] = true
	}

	topDigestSlice := util.KeysOfStringSet(topDigestMap)
	allDigestsSlice := util.KeysOfStringSet(allDigests)
	thumbs := diffStore.ThumbAbsPath(allDigestsSlice)
	full := diffStore.AbsPath(allDigestsSlice)

	// Fill in the thumbnail URLS in our GUI response struct.
	for _, t := range topDigests {
		t.Thumb = pathToURLConverter(thumbs[t.Digest])
	}

	for _, l := range leftDigests {
		l.Thumb = pathToURLConverter(thumbs[l.Digest])
	}

	grid := [][]*PolyTestDiffInfo{}
	for _, l := range leftDigests {
		row := []*PolyTestDiffInfo{}
		diffs, err := diffStore.Get(l.Digest, topDigestSlice)
		if err != nil {
			glog.Errorf("Failed to do diffs: %s", err)
			continue
		}
		for _, t := range topDigests {
			d := diffs[t.Digest]
			row = append(row, &PolyTestDiffInfo{
				Test:             req.Test,
				Thumb:            pathToURLConverter(d.ThumbnailPixelDiffFilePath),
				TopDigest:        t.Digest,
				LeftDigest:       l.Digest,
				NumDiffPixels:    d.NumDiffPixels,
				PixelDiffPercent: d.PixelDiffPercent,
				MaxRGBADiffs:     d.MaxRGBADiffs,
				DiffImg:          pathToURLConverter(d.PixelDiffFilePath),
				TopImg:           pathToURLConverter(full[t.Digest]),
				LeftImg:          pathToURLConverter(full[l.Digest]),
			})

		}
		grid = append(grid, row)
	}

	p := PolyTestGUI{
		Top:       topDigests,
		Left:      leftDigests,
		Grid:      grid,
		TopTotal:  topTotal,
		LeftTotal: leftTotal,
	}
	if len(p.Top) == 0 || len(p.Left) == 0 {
		p.Message = "Failed to find images that match those filters."
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(p); err != nil {
		util.ReportError(w, r, err, "Failed to encode result")
	}
}

// PolyTriageRequest is the form of the JSON posted to polyTriageHandler.
type PolyTriageRequest struct {
	Test    string   `json:"test"`
	Digest  []string `json:"digest"`
	Status  string   `json:"status"`
	All     bool     `json:"all"` // Ignore Digest and instead use the query, filter, and include.
	Query   string   `json:"query"`
	Filter  string   `json:"filter"`
	Include bool     `json:"include"` // Include ignored digests.
}

// polyTriageHandler handles a request to change the triage status of one or more
// digests of one test.
//
// It accepts a POST'd JSON serialization of PolyTriageRequest and updates
// the expectations.
func polyTriageHandler(w http.ResponseWriter, r *http.Request) {
	req := &PolyTriageRequest{}
	if err := parseJson(r, req); err != nil {
		util.ReportError(w, r, err, "Failed to parse JSON request.")
		return
	}
	glog.Infof("Triage request: %#v", req)
	user := login.LoggedInAs(r)
	if user == "" {
		util.ReportError(w, r, fmt.Errorf("Not logged in."), "You must be logged in to triage.")
		return
	}

	// Build the expecations change request from the list of digests passed in.
	digests := req.Digest

	// Or build the expectations change request from filter, query, and include.
	if req.All {
		exp, err := expStore.Get()
		if err != nil {
			util.ReportError(w, r, err, "Failed to load expectations.")
			return
		}
		e := exp.Tests[req.Test]
		ignores := []url.Values{}

		if req.Include {
			allIgnores, err := ignoreStore.List()
			if err != nil {
				util.ReportError(w, r, err, "Failed to load ignore rules.")
				return
			}
			for _, i := range allIgnores {
				q, _ := url.ParseQuery(i.Query)
				ignores = append(ignores, q)
			}
		}
		ii, _, err := imgInfo(req.Filter, req.Query, req.Test, e, -1, req.Include, ignores, false, "", "")
		digests = []string{}
		for _, d := range ii {
			digests = append(digests, d.Digest)
		}
	}

	// Label the digests.
	labelledDigests := map[string]types.Label{}
	for _, d := range digests {
		labelledDigests[d] = types.LabelFromString(req.Status)
	}

	tc := map[string]types.TestClassification{
		req.Test: labelledDigests,
	}
	// If the analyzer is running then use that to update the expectations.
	if *startAnalyzer {
		_, err := analyzer.SetDigestLabels(tc, user)
		if err != nil {
			util.ReportError(w, r, err, "Failed to set the expectations.")
			return
		}
	} else {
		// Otherwise update the expectations directly.
		if err := expStore.AddChange(tc, user); err != nil {
			util.ReportError(w, r, err, "Failed to store the updated expectations.")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(map[string]string{}); err != nil {
		util.ReportError(w, r, err, "Failed to encode result")
	}
}

func safeGet(paramset map[string][]string, key string) []string {
	if ret, ok := paramset[key]; ok {
		sort.Strings(ret)
		return ret
	} else {
		return []string{}
	}
}

// PerParamCompare is used in PolyDetailsGUI
type PerParamCompare struct {
	Name string   `json:"name"` // Name of the parameter.
	Top  []string `json:"top"`  // All the parameter values that appear for the top digest.
	Left []string `json:"left"` // All the parameter values that appear for the left digest.
}

// PolyDetailsGUI is used in the JSON returned from polyDetailsHandler. It
// represents the known information about a single digest for a given test.
type PolyDetailsGUI struct {
	TopStatus  string             `json:"topStatus"`
	LeftStatus string             `json:"leftStatus"`
	Params     []*PerParamCompare `json:"params"`
}

// polyDetailsHandler handles requests about individual digests in a test.
//
// It expects a request with the following query parameters:
//
//   test - The name of the test.
//   top  - A digest in the test.
//   left - A digest in the test.
//
// The response looks like:
//   {
//     topStatus: "untriaged",
//     leftStatus: "positive",
//     params: [
//       {
//         "name": "config",
//         "top" : ["8888", "565"],
//         "left": ["gpu"],
//       },
//       ...
//     ]
//   }
func polyDetailsHandler(w http.ResponseWriter, r *http.Request) {
	tile, err := tileStore.Get(0, -1)
	if err != nil {
		util.ReportError(w, r, err, "Failed to load tile")
		return
	}
	if err := r.ParseForm(); err != nil {
		util.ReportError(w, r, err, "Failed to parse form values")
		return
	}
	top := r.Form.Get("top")
	left := r.Form.Get("left")
	if top == "" || left == "" {
		util.ReportError(w, r, fmt.Errorf("Missing the top or left query parameter: %s %s", top, left), "No digests specified.")
		return
	}
	test := r.Form.Get("test")
	if test == "" {
		util.ReportError(w, r, fmt.Errorf("Missing the test query parameter."), "No test name specified.")
		return
	}
	exp, err := expStore.Get()
	if err != nil {
		util.ReportError(w, r, err, "Failed to load expectations.")
		return
	}

	ret := PolyDetailsGUI{
		TopStatus:  exp.Classification(test, top).String(),
		LeftStatus: exp.Classification(test, left).String(),
		Params:     []*PerParamCompare{},
	}

	topParamSet := map[string][]string{}
	leftParamSet := map[string][]string{}

	// Now build out the ParamSet for each digest.
	tally := tallies.ByTrace()
	for id, tr := range tile.Traces {
		traceTally, ok := tally[id]
		if !ok {
			continue
		}
		if tr.Params()[types.PRIMARY_KEY_FIELD] == test {
			if _, ok := (*traceTally)[top]; ok {
				topParamSet = util.AddParamsToParamSet(topParamSet, tr.Params())
			}
		}
		if tr.Params()[types.PRIMARY_KEY_FIELD] == test {
			if _, ok := (*traceTally)[left]; ok {
				leftParamSet = util.AddParamsToParamSet(leftParamSet, tr.Params())
			}
		}
	}

	keys := util.UnionStrings(util.KeysOfParamSet(topParamSet), util.KeysOfParamSet(leftParamSet))
	sort.Strings(keys)
	for _, k := range keys {
		ret.Params = append(ret.Params, &PerParamCompare{
			Name: k,
			Top:  safeGet(topParamSet, k),
			Left: safeGet(leftParamSet, k),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(ret); err != nil {
		util.ReportError(w, r, err, "Failed to encode result")
	}
}

func polyParamsHandler(w http.ResponseWriter, r *http.Request) {
	tile, err := tileStore.Get(0, -1)
	if err != nil {
		util.ReportError(w, r, err, "Failed to load tile")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(tile.ParamSet); err != nil {
		util.ReportError(w, r, err, "Failed to encode result")
	}
}

// makeResourceHandler creates a static file handler that sets a caching policy.
func makeResourceHandler() func(http.ResponseWriter, *http.Request) {
	fileServer := http.FileServer(http.Dir(*resourcesDir))
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Cache-Control", string(300))
		fileServer.ServeHTTP(w, r)
	}
}

// Init figures out where the resources are and then loads the templates.
func Init() {
	if *resourcesDir == "" {
		_, filename, _, _ := runtime.Caller(0)
		*resourcesDir = filepath.Join(filepath.Dir(filename), "../..")
	}
	loadTemplates()
}
